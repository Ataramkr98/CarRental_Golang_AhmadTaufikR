package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"car-rental-backend/models"
)

// DB is the shared GORM handle used by the service layer.
var DB *gorm.DB

// Close releases the current database pool. Primarily useful for test
// isolation, and safe to call when no database is connected.
func Close() {
	if DB == nil {
		return
	}
	if sqlDB, err := DB.DB(); err == nil {
		_ = sqlDB.Close()
	}
	DB = nil
}

// Connect opens PostgreSQL, migrates the schema and seeds demo data.
func Connect() {
	// setupApp is called repeatedly by the test suite, so release the previous
	// pool before replacing the package-level handle.
	Close()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	// TranslateError makes GORM map driver-specific failures onto sentinel
	// errors (gorm.ErrDuplicatedKey, gorm.ErrForeignKeyViolated), so the
	// service layer can branch on a type instead of parsing message text.
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("failed to get sql.DB: %v", err)
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)
	sqlDB.SetConnMaxIdleTime(15 * time.Minute)

	if err := db.AutoMigrate(
		&models.User{},
		&models.Car{},
		&models.Customer{},
		&models.Rental{},
		&models.RentalEvent{},
		&models.CashFlow{},
	); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	DB = db
	warnOnInsecureDefaults()

	// RESEED=true wipes the application tables and rebuilds the demo dataset.
	// It is used by the integration test suite for per-test isolation, and is
	// available as a one-off maintenance flag.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("RESEED")), "true") {
		if err := Reset(); err != nil {
			log.Fatalf("failed to reset demo data: %v", err)
		}
		log.Println("demo data reset (RESEED=true)")
	}

	seed()
	log.Println("database connected, migrated and seeded")
}

// Reset deletes every application row so the demo dataset can be rebuilt.
// Rows are removed in dependency order to respect the foreign keys.
func Reset() error {
	if DB == nil {
		return errors.New("database is not connected")
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		tables := []any{
			&models.RentalEvent{},
			&models.CashFlow{},
			&models.Rental{},
			&models.Customer{},
			&models.Car{},
			&models.User{},
		}
		for _, table := range tables {
			if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(table).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// jwtSecret is resolved once per process from JWT_SECRET, or generated randomly
// when that variable is unset.
var (
	jwtSecretOnce sync.Once
	jwtSecret     []byte
)

// minJWTSecretLength is the shortest signing key accepted. HMAC-SHA256 wants at
// least 32 bytes of key material, and a shorter value is almost always a
// placeholder somebody forgot to replace.
const minJWTSecretLength = 32

// JWTSecret resolves the token signing secret.
//
// When JWT_SECRET is set it must be at least 32 characters; anything shorter is
// rejected at boot rather than silently accepted. When it is unset, a
// cryptographically random secret is generated for the lifetime of the process.
//
// Both halves are deliberate. A published fallback is a signing key that exists
// in every clone of this repository, so anyone could mint an admin token against
// a deployment that forgot the variable; refusing a too-short value stops the
// other common mistake of shipping `change-me`. The cost of the generated path
// is that sessions end when the process restarts, which is announced at boot.
func JWTSecret() []byte {
	jwtSecretOnce.Do(func() {
		if secret := strings.TrimSpace(os.Getenv("JWT_SECRET")); secret != "" {
			if len(secret) < minJWTSecretLength {
				log.Fatalf(
					"FATAL: JWT_SECRET is %d characters; it must be at least %d. Generate one with: openssl rand -base64 48",
					len(secret), minJWTSecretLength,
				)
			}
			jwtSecret = []byte(secret)
			return
		}

		generated := make([]byte, minJWTSecretLength)
		if _, err := rand.Read(generated); err != nil {
			// crypto/rand failing is not a recoverable condition for a signing key.
			log.Fatal("FATAL: could not generate a JWT secret — set JWT_SECRET explicitly")
		}
		jwtSecret = []byte(hex.EncodeToString(generated))
		log.Println("WARNING: JWT_SECRET is not set; generated a random one for this process only")
		log.Println("         Set JWT_SECRET to keep console sessions valid across restarts")
	})
	return jwtSecret
}

// warnOnInsecureDefaults surfaces configuration that must not ship to
// production, without refusing to boot a local development environment.
func warnOnInsecureDefaults() {
	// Calling JWTSecret is what performs the resolution and its warning.
	_ = JWTSecret()
}

// ptr returns a pointer to a time, for the nullable timestamp columns.
func ptr(t time.Time) *time.Time { return &t }

// seed installs realistic demo data. Every block is guarded so restarting the
// service never duplicates records.
func seed() {
	seedUsers()
	cars := seedFleet()
	customers := seedCustomers()
	seedRentals(cars, customers)
}

// demoAccount describes one seeded console login.
type demoAccount struct {
	Name     string
	Email    string
	Password string
	Role     string
}

// demoAccounts are the logins published in the README and on the sign-in page.
// This is a portfolio deployment, so the credentials are intentionally public.
// The two accounts also demonstrate the role split: the administrator can
// delete records, the operator runs the console without that privilege.
var demoAccounts = []demoAccount{
	{Name: "Alex Morgan", Email: "admin@rental.dev", Password: "admin123", Role: "admin"},
	{Name: "Demo Operator", Email: "demo@rental.dev", Password: "demo123", Role: "operator"},
}

// seedUsers creates the demo console accounts.
//
// Each account is checked individually rather than by row count, so adding a
// new demo login later takes effect on an existing database without a reseed.
// When an account already exists its role is re-synced, which keeps seeded
// databases consistent with this table after role changes.
func seedUsers() {
	for _, account := range demoAccounts {
		var existing int64
		if err := DB.Model(&models.User{}).Where("email = ?", account.Email).Count(&existing).Error; err != nil {
			log.Fatalf("failed to check for %s: %v", account.Email, err)
		}
		if existing > 0 {
			if err := DB.Model(&models.User{}).Where("email = ?", account.Email).
				Update("role", account.Role).Error; err != nil {
				log.Fatalf("failed to sync role for %s: %v", account.Email, err)
			}
			continue
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(account.Password), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("failed to hash password for %s: %v", account.Email, err)
		}

		user := models.User{
			Name:     account.Name,
			Email:    account.Email,
			Password: string(hash),
			Role:     account.Role,
		}
		if err := DB.Create(&user).Error; err != nil {
			log.Fatalf("failed to seed user %s: %v", account.Email, err)
		}
	}
}

// seedFleet creates the demo vehicles. Statuses are kept consistent with the
// rentals created later (one car is out, one is reserved).
func seedFleet() []models.Car {
	image := func(id string) string {
		return "https://images.unsplash.com/" + id + "?auto=format&fit=crop&w=900&q=70"
	}

	cars := []models.Car{
		{Brand: "Toyota", Model: "GR Supra", Year: 2024, Plate: "B 1001 GRA", DailyPrice: 95, Status: "rented", Image: image("photo-1627008119017-f89d9704a799")},
		{Brand: "Chevrolet", Model: "Camaro SS", Year: 2023, Plate: "B 1002 CAM", DailyPrice: 88, Status: "reserved", Image: image("photo-1654689704308-ec20e7fc7394")},
		{Brand: "Ford", Model: "Mustang GT", Year: 2023, Plate: "B 1003 MST", DailyPrice: 90, Status: "available", Image: image("photo-1494976388531-d1058494cdd8")},
		{Brand: "Lamborghini", Model: "Huracán EVO", Year: 2024, Plate: "B 1004 LAM", DailyPrice: 320, Status: "available", Image: image("photo-1552519507-da3b142c6e3d")},
		{Brand: "Honda", Model: "Civic Type R", Year: 2024, Plate: "B 1005 CIV", DailyPrice: 70, Status: "maintenance", Image: image("photo-1572471372724-a7c1c9450c21")},
		{Brand: "Tesla", Model: "Model 3 Long Range", Year: 2024, Plate: "B 1006 TSL", DailyPrice: 110, Status: "available", Image: image("photo-1560958089-b8a1929cea89")},
		{Brand: "BMW", Model: "M4 Competition", Year: 2023, Plate: "B 1007 BMW", DailyPrice: 150, Status: "available", Image: image("photo-1728060838342-cb9744a27d1b")},
		{Brand: "Mercedes-Benz", Model: "C 300 AMG Line", Year: 2024, Plate: "B 1008 MBZ", DailyPrice: 140, Status: "available", Image: image("photo-1618843479313-40f8afb4b4d8")},
		{Brand: "Audi", Model: "RS5 Coupé", Year: 2024, Plate: "B 1009 AUD", DailyPrice: 165, Status: "available", Image: image("photo-1606152421802-db97b9c7a11b")},
		{Brand: "Porsche", Model: "911 Carrera", Year: 2023, Plate: "B 1010 PSC", DailyPrice: 190, Status: "available", Image: image("photo-1503376780353-7e6692767b70")},
		{Brand: "Mazda", Model: "MX-5 Miata", Year: 2024, Plate: "B 1011 MZD", DailyPrice: 65, Status: "available", Image: image("photo-1552615526-40e47a79f9d7")},
		{Brand: "Volkswagen", Model: "Golf GTI", Year: 2023, Plate: "B 1012 VWG", DailyPrice: 72, Status: "available", Image: image("photo-1605475300127-0a31e8273bc2")},
	}

	var count int64
	if err := DB.Model(&models.Car{}).Count(&count).Error; err != nil {
		log.Fatalf("failed to count fleet: %v", err)
	}
	if count > 0 {
		// Sync updated image URLs onto existing database rows
		for _, c := range cars {
			_ = DB.Model(&models.Car{}).Where("plate = ?", c.Plate).Update("image", c.Image).Error
		}
		var existing []models.Car
		DB.Order("id asc").Find(&existing)
		return existing
	}

	if err := DB.Create(&cars).Error; err != nil {
		log.Fatalf("failed to seed fleet: %v", err)
	}
	return cars
}

// seedCustomers creates the demo customer directory.
func seedCustomers() []models.Customer {
	var count int64
	if err := DB.Model(&models.Customer{}).Count(&count).Error; err != nil {
		log.Fatalf("failed to count customers: %v", err)
	}
	if count > 0 {
		var existing []models.Customer
		DB.Order("id asc").Find(&existing)
		return existing
	}

	customers := []models.Customer{
		{Name: "Budi Santoso", Email: "budi.santoso@example.com", Phone: "0812-3456-7890", License: "SIM-1234"},
		{Name: "Siti Rahma", Email: "siti.rahma@example.com", Phone: "0812-9876-5432", License: "SIM-5678"},
		{Name: "Andi Wijaya", Email: "andi.wijaya@example.com", Phone: "0813-7778-8899", License: "SIM-9012"},
		{Name: "Dewi Lestari", Email: "dewi.lestari@example.com", Phone: "0813-2211-4455", License: "SIM-3456"},
		{Name: "Rizky Pratama", Email: "rizky.pratama@example.com", Phone: "0815-6677-8899", License: "SIM-7788"},
		{Name: "Maya Kusuma", Email: "maya.kusuma@example.com", Phone: "0817-1122-3344", License: "SIM-2233"},
		{Name: "Fajar Nugroho", Email: "fajar.nugroho@example.com", Phone: "0818-5566-7788", License: "SIM-6677"},
		{Name: "Nadia Putri", Email: "nadia.putri@example.com", Phone: "0819-3344-5566", License: "SIM-4455"},
	}

	if err := DB.Create(&customers).Error; err != nil {
		log.Fatalf("failed to seed customers: %v", err)
	}
	return customers
}

// seedRentals builds a rental history that spans six months so the dashboard
// chart, activity log and pagination all show meaningful data.
func seedRentals(cars []models.Car, customers []models.Customer) {
	var count int64
	if err := DB.Model(&models.Rental{}).Count(&count).Error; err != nil {
		log.Fatalf("failed to count rentals: %v", err)
	}
	if count > 0 || len(cars) < 12 || len(customers) < 8 {
		return
	}

	now := time.Now()
	car := func(i int) uint { return cars[i].ID }
	customer := func(i int) uint { return customers[i].ID }

	type historyRow struct {
		carIndex, customerIndex int
		startDays, endDays      int
		lateFee                 float64
		odometerOut, odometerIn int
		note                    string
	}

	// Completed rentals, newest last. The spread across ~150 days gives the
	// revenue chart a realistic shape.
	history := []historyRow{
		{2, 2, -150, -146, 0, 22000, 22480, "Returned on time, interior cleaned"},
		{5, 3, -132, -128, 0, 15400, 15890, "Returned on time"},
		{6, 4, -118, -112, 0, 9800, 10520, "Returned on time"},
		{7, 5, -96, -92, 0, 32100, 32560, "Returned on time, small scratch noted"},
		{9, 1, -74, -70, 0, 11200, 11780, "Returned on time"},
		{2, 0, -58, -55, 0, 22480, 22810, "Returned on time"},
		{5, 6, -41, -37, 0, 15890, 16340, "Returned on time"},
		{6, 2, -30, -27, 75, 10520, 10990, "Returned 1 day late — late fee charged"},
		{7, 7, -19, -15, 0, 32560, 33010, "Returned on time"},
		{5, 4, -12, -9, 0, 16340, 16650, "Returned on time"},
		{2, 1, -6, -3, 0, 22810, 23120, "Returned on time"},
	}

	rentals := make([]models.Rental, 0, len(history)+4)
	for _, row := range history {
		start := now.AddDate(0, 0, row.startDays)
		end := now.AddDate(0, 0, row.endDays)
		paidAt := start.AddDate(0, 0, -1)
		returnedAt := end.AddDate(0, 0, 1)
		rentals = append(rentals, models.Rental{
			CarID: car(row.carIndex), CustomerID: customer(row.customerIndex),
			StartDate: start, EndDate: end,
			TotalPrice: float64(end.Sub(start).Hours()/24) * cars[row.carIndex].DailyPrice,
			LateFee:    row.lateFee, Status: "returned",
			PaymentStatus: "paid", PaymentMethod: "Xendit Invoice",
			InvoiceID:     "inv-mock-h" + time.Now().Format("150405") + "-" + strconv.Itoa(len(rentals)+1),
			PaidAt:        ptr(paidAt),
			DriverName:    customers[row.customerIndex].Name,
			DriverLicense: customers[row.customerIndex].License,
			DriverPhone:   customers[row.customerIndex].Phone,
			OdometerOut:   row.odometerOut, OdometerIn: row.odometerIn,
			PickupNote: "Full tank, no damage", ReturnNote: row.note,
			BorrowedAt: ptr(start), ReturnedAt: ptr(returnedAt),
		})
	}

	// Live rentals: one on the road, one paid and awaiting hand-over.
	rentals = append(rentals,
		models.Rental{
			CarID: car(0), CustomerID: customer(0),
			StartDate: now.AddDate(0, 0, -2), EndDate: now.AddDate(0, 0, 3),
			TotalPrice: 475, Status: "active",
			PaymentStatus: "paid", PaymentMethod: "Xendit Invoice",
			InvoiceID: "inv-mock-active-1", PaidAt: ptr(now.AddDate(0, 0, -3)),
			DriverName: "Budi Santoso", DriverLicense: "SIM-1234", DriverPhone: "0812-3456-7890",
			OdometerOut: 12000, PickupNote: "Full tank, no scratches",
			BorrowedAt: ptr(now.AddDate(0, 0, -2)),
		},
		models.Rental{
			CarID: car(1), CustomerID: customer(1),
			StartDate: now.AddDate(0, 0, 1), EndDate: now.AddDate(0, 0, 4),
			TotalPrice: 264, Status: "paid",
			PaymentStatus: "paid", PaymentMethod: "Xendit Invoice",
			InvoiceID: "inv-mock-paid-2", PaidAt: ptr(now.AddDate(0, 0, -1)),
			DriverName: "Siti Rahma", DriverLicense: "SIM-5678", DriverPhone: "0812-9876-5432",
		},
		// Awaiting payment — the car is still bookable, so it stays available.
		models.Rental{
			CarID: car(3), CustomerID: customer(0),
			StartDate: now.AddDate(0, 0, 2), EndDate: now.AddDate(0, 0, 5),
			TotalPrice: 960, Status: "pending", PaymentStatus: "unpaid",
			DriverName: "Budi Santoso",
		},
		// Cancelled before payment.
		models.Rental{
			CarID: car(8), CustomerID: customer(5),
			StartDate: now.AddDate(0, 0, -4), EndDate: now.AddDate(0, 0, -2),
			TotalPrice: 330, Status: "cancelled", PaymentStatus: "unpaid",
		},
	)

	if err := DB.Create(&rentals).Error; err != nil {
		log.Fatalf("failed to seed rentals: %v", err)
	}

	seedRentalEvents(rentals)
	seedCashFlow(rentals, cars)
}

// seedRentalEvents writes the timeline for each seeded rental.
func seedRentalEvents(rentals []models.Rental) {
	events := make([]models.RentalEvent, 0, len(rentals)*3)

	for _, rental := range rentals {
		events = append(events, models.RentalEvent{
			RentalID: rental.ID, ToStatus: "pending",
			Note: "Booking created — awaiting payment", CreatedAt: rental.CreatedAt,
		})

		if rental.PaymentStatus != "paid" {
			if rental.Status == "cancelled" {
				events = append(events, models.RentalEvent{
					RentalID: rental.ID, FromStatus: "pending", ToStatus: "cancelled",
					Note: "Booking cancelled by the administrator", CreatedAt: rental.UpdatedAt,
				})
			}
			continue
		}

		paidAt := rental.CreatedAt
		if rental.PaidAt != nil {
			paidAt = *rental.PaidAt
		}
		events = append(events, models.RentalEvent{
			RentalID: rental.ID, FromStatus: "pending", ToStatus: "paid",
			Note: "Payment received", CreatedAt: paidAt,
		})

		if rental.BorrowedAt != nil {
			events = append(events, models.RentalEvent{
				RentalID: rental.ID, FromStatus: "paid", ToStatus: "active",
				Note: "Car handed over to " + rental.DriverName, CreatedAt: *rental.BorrowedAt,
			})
		}
		if rental.ReturnedAt != nil {
			note := "Car returned"
			if rental.LateFee > 0 {
				note = "Car returned late — late fee charged"
			}
			events = append(events, models.RentalEvent{
				RentalID: rental.ID, FromStatus: "active", ToStatus: "returned",
				Note: note, CreatedAt: *rental.ReturnedAt,
			})
		}
	}

	if err := DB.Create(&events).Error; err != nil {
		log.Fatalf("failed to seed rental events: %v", err)
	}
}

// seedCashFlow derives income from the seeded rentals and adds realistic
// operating expenses so the ledger and chart are populated.
func seedCashFlow(rentals []models.Rental, cars []models.Car) {
	var count int64
	if err := DB.Model(&models.CashFlow{}).Count(&count).Error; err != nil {
		log.Fatalf("failed to count cash flow: %v", err)
	}
	if count > 0 {
		return
	}

	now := time.Now()
	entries := make([]models.CashFlow, 0, len(rentals)*2+16)

	label := func(index int) string {
		if index < 0 || index >= len(cars) {
			return "Vehicle"
		}
		return cars[index].Brand + " " + cars[index].Model
	}
	carIndexByID := make(map[uint]int, len(cars))
	for index, car := range cars {
		carIndexByID[car.ID] = index
	}

	for _, rental := range rentals {
		if rental.PaymentStatus != "paid" {
			continue
		}
		when := rental.CreatedAt
		if rental.PaidAt != nil {
			when = *rental.PaidAt
		}
		entries = append(entries, models.CashFlow{
			Type: "in", Category: "rental_income", Amount: rental.TotalPrice,
			Description:   "Rental #" + strconv.Itoa(int(rental.ID)) + " — " + label(carIndexByID[rental.CarID]),
			ReferenceType: "rental", ReferenceID: rental.ID, Date: when,
		})
		if rental.LateFee > 0 && rental.ReturnedAt != nil {
			entries = append(entries, models.CashFlow{
				Type: "in", Category: "late_fee", Amount: rental.LateFee,
				Description:   "Late fee — rental #" + strconv.Itoa(int(rental.ID)),
				ReferenceType: "rental", ReferenceID: rental.ID, Date: *rental.ReturnedAt,
			})
		}
	}

	// Operating expenses spread across the same six-month window.
	expenses := []struct {
		category    string
		amount      float64
		description string
		daysAgo     int
	}{
		{"maintenance", 420, "Scheduled service — Toyota GR Supra", 148},
		{"fuel", 260, "Fleet fuel card top-up", 140},
		{"salary", 2400, "Operations staff — monthly payroll", 135},
		{"marketing", 380, "Search ads — weekend promotion", 120},
		{"maintenance", 275, "Brake pads and tyres — BMW M4", 104},
		{"fuel", 240, "Fleet fuel card top-up", 88},
		{"salary", 2400, "Operations staff — monthly payroll", 75},
		{"maintenance", 690, "Full service — Porsche 911", 62},
		{"marketing", 450, "Social campaign — summer rentals", 51},
		{"fuel", 255, "Fleet fuel card top-up", 38},
		{"salary", 2400, "Operations staff — monthly payroll", 21},
		{"maintenance", 180, "Oil change and inspection — Tesla Model 3", 14},
		{"fuel", 210, "Fleet fuel card top-up", 6},
		{"marketing", 320, "Retargeting campaign", 3},
	}
	for _, expense := range expenses {
		entries = append(entries, models.CashFlow{
			Type: "out", Category: expense.category, Amount: expense.amount,
			Description: expense.description, ReferenceType: "manual",
			Date: now.AddDate(0, 0, -expense.daysAgo),
		})
	}

	if err := DB.Create(&entries).Error; err != nil {
		log.Fatalf("failed to seed cash flow: %v", err)
	}
}
