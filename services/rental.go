package services

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"car-rental-backend/config"
	"car-rental-backend/models"
	"car-rental-backend/xendit"
)

// RentalStatuses enumerates every state in the rental lifecycle.
var RentalStatuses = map[string]bool{
	"pending":   true,
	"paid":      true,
	"active":    true,
	"returned":  true,
	"cancelled": true,
}

// RentalFilter narrows a rental listing.
type RentalFilter struct {
	Status string
	Search string
	Sort   string
	Page   Page
	Paged  bool
}

// BookingInput is the public booking payload.
type BookingInput struct {
	CarID     uint      `json:"car_id"`
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
	Customer  struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Phone   string `json:"phone"`
		License string `json:"license"`
	} `json:"customer"`
}

// BorrowInput captures the hand-over details recorded when a car leaves.
type BorrowInput struct {
	DriverName    string `json:"driver_name"`
	DriverLicense string `json:"driver_license"`
	DriverPhone   string `json:"driver_phone"`
	OdometerOut   int    `json:"odometer_out"`
	PickupNote    string `json:"pickup_note"`
}

// ReturnInput captures the condition recorded when a car comes back.
type ReturnInput struct {
	OdometerIn int    `json:"odometer_in"`
	ReturnNote string `json:"return_note"`
}

// TrackingEntry is one car currently on the road.
type TrackingEntry struct {
	ID            uint            `json:"id"`
	Car           models.Car      `json:"car"`
	Customer      models.Customer `json:"customer"`
	DriverName    string          `json:"driver_name"`
	DriverLicense string          `json:"driver_license"`
	DriverPhone   string          `json:"driver_phone"`
	BorrowedAt    *time.Time      `json:"borrowed_at"`
	DueDate       time.Time       `json:"due_date"`
	OdometerOut   int             `json:"odometer_out"`
	Overdue       bool            `json:"overdue"`
	DaysLeft      int             `json:"days_left"`
	OverdueDays   int             `json:"overdue_days"`
}

// preloadRental is the shared eager-loading shape used across the API.
func preloadRental(db *gorm.DB) *gorm.DB {
	return db.Preload("Car").Preload("Customer").
		Preload("Events", func(tx *gorm.DB) *gorm.DB { return tx.Order("created_at asc, id asc") })
}

// ListRentals returns rentals for the admin console.
func ListRentals(filter RentalFilter) (any, error) {
	query := config.DB.Model(&models.Rental{})

	if status := normalizeStatus(filter.Status); status != "" {
		if !RentalStatuses[status] {
			return nil, Invalid("invalid rental status")
		}
		query = query.Where("status = ?", status)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.
			Joins("LEFT JOIN cars ON cars.id = rentals.car_id").
			Joins("LEFT JOIN customers ON customers.id = rentals.customer_id").
			Where("LOWER(cars.brand) LIKE ? OR LOWER(cars.model) LIKE ? OR LOWER(cars.plate) LIKE ? OR LOWER(customers.name) LIKE ?",
				like, like, like, like)
	}

	if !filter.Paged {
		var rentals []models.Rental
		if err := preloadRental(applyRentalSort(query, filter.Sort)).Find(&rentals).Error; err != nil {
			return nil, err
		}
		return rentals, nil
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var rentals []models.Rental
	if err := preloadRental(filter.Page.Apply(applyRentalSort(query, filter.Sort))).Find(&rentals).Error; err != nil {
		return nil, err
	}
	return NewPageResult(rentals, total, filter.Page), nil
}

func applyRentalSort(query *gorm.DB, sort string) *gorm.DB {
	switch strings.TrimSpace(sort) {
	case "oldest":
		return query.Order("rentals.id asc")
	case "due_asc":
		return query.Order("rentals.end_date asc")
	case "value_desc":
		return query.Order("rentals.total_price desc")
	default:
		return query.Order("rentals.id desc")
	}
}

// GetRental loads a single rental with its car, customer and timeline.
func GetRental(id uint) (*models.Rental, error) {
	var rental models.Rental
	if err := preloadRental(config.DB).First(&rental, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("rental not found")
		}
		return nil, err
	}
	return &rental, nil
}

func normalizeBooking(in *BookingInput) {
	in.Customer.Name = strings.TrimSpace(in.Customer.Name)
	in.Customer.Email = strings.ToLower(strings.TrimSpace(in.Customer.Email))
	in.Customer.Phone = strings.TrimSpace(in.Customer.Phone)
	in.Customer.License = strings.TrimSpace(in.Customer.License)
}

func validateBooking(in BookingInput) error {
	if in.CarID == 0 {
		return Invalid("please choose a car")
	}
	if in.StartDate.IsZero() || in.EndDate.IsZero() || !in.EndDate.After(in.StartDate) {
		return Invalid("the return date must be after the pick-up date")
	}

	location := in.StartDate.Location()
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	if in.StartDate.Before(today) {
		return Invalid("the pick-up date cannot be in the past")
	}
	if in.EndDate.Sub(in.StartDate) > 366*24*time.Hour {
		return Invalid("a rental period cannot exceed 366 days")
	}

	if in.Customer.Name == "" || in.Customer.Email == "" || in.Customer.Phone == "" || in.Customer.License == "" {
		return Invalid("name, email, phone and driving licence are all required")
	}
	if err := validateTextLengths(map[string]string{
		"name": in.Customer.Name, "email": in.Customer.Email,
		"phone": in.Customer.Phone, "licence": in.Customer.License,
	}); err != nil {
		return err
	}
	if !validEmail(in.Customer.Email) {
		return Invalid("please enter a valid email address")
	}
	return nil
}

// CreateBooking creates a pending rental and reuses an existing customer
// record when the same person books again.
//
// The returned receipt carries a one-time checkout token: the storefront has no
// accounts, so that token is what proves later pay / status calls come from the
// same browser that made this booking.
func CreateBooking(in BookingInput) (*BookingReceipt, error) {
	normalizeBooking(&in)
	if err := validateBooking(in); err != nil {
		return nil, err
	}

	var (
		rental models.Rental
		car    models.Car
		token  string
	)

	err := config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&car, in.CarID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NotFound("that car is no longer listed")
			}
			return err
		}
		if car.Status != "available" {
			return Conflict("that car has just been booked by someone else")
		}

		var customer models.Customer
		result := tx.Where("email = ? AND phone = ?", in.Customer.Email, in.Customer.Phone).Limit(1).Find(&customer)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			customer = models.Customer{
				Name: in.Customer.Name, Email: in.Customer.Email,
				Phone: in.Customer.Phone, License: in.Customer.License,
			}
			if err := tx.Create(&customer).Error; err != nil {
				return err
			}
		}

		rental = models.Rental{
			CarID: car.ID, CustomerID: customer.ID,
			StartDate: in.StartDate, EndDate: in.EndDate,
			TotalPrice: float64(daysBetween(in.StartDate, in.EndDate)) * car.DailyPrice,
			Status:     "pending", PaymentStatus: "unpaid",
			DriverName: in.Customer.Name, DriverLicense: in.Customer.License, DriverPhone: in.Customer.Phone,
		}
		if err := tx.Create(&rental).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.RentalEvent{
			RentalID: rental.ID, ToStatus: "pending",
			Note: "Booking created — awaiting payment",
		}).Error; err != nil {
			return err
		}
		issued, err := IssueCheckoutToken(tx, &rental)
		if err != nil {
			return err
		}
		token = issued
		return nil
	})
	if err != nil {
		return nil, err
	}

	full, err := GetRental(rental.ID)
	if err != nil {
		return nil, err
	}
	return &BookingReceipt{PublicBookingResult: PublicBooking(full), CheckoutToken: token}, nil
}

// PublicBookingResult is the sanitised view returned to an anonymous booker.
// It deliberately omits customer and driver details.
type PublicBookingResult struct {
	ID            uint       `json:"id"`
	CarID         uint       `json:"car_id"`
	Car           models.Car `json:"car"`
	StartDate     time.Time  `json:"start_date"`
	EndDate       time.Time  `json:"end_date"`
	TotalPrice    float64    `json:"total_price"`
	Status        string     `json:"status"`
	PaymentStatus string     `json:"payment_status"`
}

// PublicBooking projects a rental down to the fields a guest may see.
func PublicBooking(rental *models.Rental) PublicBookingResult {
	return PublicBookingResult{
		ID: rental.ID, CarID: rental.CarID, Car: rental.Car,
		StartDate: rental.StartDate, EndDate: rental.EndDate,
		TotalPrice: rental.TotalPrice, Status: rental.Status,
		PaymentStatus: rental.PaymentStatus,
	}
}

// BookingReceipt is what a guest receives after creating a booking. It carries
// the sanitised rental plus the one-time checkout token that authorises this
// browser's payment calls. The token is never returned again.
type BookingReceipt struct {
	PublicBookingResult
	CheckoutToken string `json:"checkout_token"`
}

// lateFeeMultiplier is the fraction of the daily price charged per overdue day.
func lateFeeMultiplier() float64 {
	if value := os.Getenv("LATE_FEE_MULTIPLIER"); value != "" {
		if multiplier, err := strconv.ParseFloat(value, 64); err == nil && multiplier >= 0 &&
			!math.IsNaN(multiplier) && !math.IsInf(multiplier, 0) {
			return multiplier
		}
	}
	return 0.5
}

// BorrowRental hands a paid, reserved car over to its driver and moves the
// rental into the tracked `active` state.
func BorrowRental(id uint, in BorrowInput) (*models.Rental, error) {
	in.DriverName = strings.TrimSpace(in.DriverName)
	in.DriverLicense = strings.TrimSpace(in.DriverLicense)
	in.DriverPhone = strings.TrimSpace(in.DriverPhone)
	in.PickupNote = strings.TrimSpace(in.PickupNote)
	if in.OdometerOut < 0 {
		return nil, Invalid("the odometer reading cannot be negative")
	}
	if err := validateTextLengths(map[string]string{
		"driver name": in.DriverName, "driver phone": in.DriverPhone,
		"licence": in.DriverLicense, "pickup note": in.PickupNote,
	}); err != nil {
		return nil, err
	}

	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var rental models.Rental
		if err := tx.Preload("Car").First(&rental, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NotFound("rental not found")
			}
			return err
		}
		if rental.Status != "paid" || rental.PaymentStatus != "paid" {
			return Conflict("this rental must be paid before the car can be handed over")
		}
		if rental.Car.Status == "maintenance" {
			return Conflict("this car is in maintenance and cannot be handed over")
		}

		if in.DriverName != "" {
			rental.DriverName = in.DriverName
		}
		if in.DriverLicense != "" {
			rental.DriverLicense = in.DriverLicense
		}
		if in.DriverPhone != "" {
			rental.DriverPhone = in.DriverPhone
		}
		if rental.DriverName == "" || rental.DriverLicense == "" || rental.DriverPhone == "" {
			return Invalid("driver name, licence and phone number are required")
		}

		now := time.Now()
		rental.OdometerOut = in.OdometerOut
		rental.PickupNote = in.PickupNote
		rental.BorrowedAt = &now
		rental.Status = "active"
		if err := tx.Save(&rental).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Car{}).Where("id = ?", rental.CarID).
			Update("status", "rented").Error; err != nil {
			return err
		}

		note := "Car handed over to " + rental.DriverName
		if in.OdometerOut > 0 {
			note += fmt.Sprintf(" (odometer %d)", in.OdometerOut)
		}
		return tx.Create(&models.RentalEvent{
			RentalID: rental.ID, FromStatus: "paid", ToStatus: "active", Note: note,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return GetRental(id)
}

// ReturnRental closes an active rental, charges any late fee and logs the
// resulting income in the same transaction.
func ReturnRental(id uint, in ReturnInput) (*models.Rental, error) {
	in.ReturnNote = strings.TrimSpace(in.ReturnNote)
	if in.OdometerIn < 0 {
		return nil, Invalid("the odometer reading cannot be negative")
	}
	if err := limitField("return note", in.ReturnNote, maxLongText); err != nil {
		return nil, err
	}

	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var rental models.Rental
		if err := tx.Preload("Car").First(&rental, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NotFound("rental not found")
			}
			return err
		}
		if rental.Status != "active" {
			return Conflict("only a car that is currently on the road can be returned")
		}
		if in.OdometerIn < rental.OdometerOut {
			return Invalid("the return odometer cannot be lower than the pick-up odometer")
		}

		now := time.Now()
		lateFee := 0.0
		overdueDays := 0
		if now.After(rental.EndDate) {
			overdueDays = int(math.Ceil(now.Sub(rental.EndDate).Hours() / 24))
			lateFee = float64(overdueDays) * rental.Car.DailyPrice * lateFeeMultiplier()
		}

		rental.OdometerIn = in.OdometerIn
		rental.ReturnNote = in.ReturnNote
		rental.ReturnedAt = &now
		rental.LateFee = lateFee
		rental.Status = "returned"
		if err := tx.Save(&rental).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Car{}).Where("id = ?", rental.CarID).
			Update("status", "available").Error; err != nil {
			return err
		}

		note := "Car returned"
		if in.OdometerIn > 0 {
			note += fmt.Sprintf(" (odometer %d)", in.OdometerIn)
		}
		if lateFee > 0 {
			note += fmt.Sprintf(" — %d day(s) overdue, late fee %.2f charged", overdueDays, lateFee)
			if err := recordCash(tx, models.CashFlow{
				Type: "in", Category: "late_fee", Amount: lateFee,
				Description: fmt.Sprintf("Late fee — rental #%d (%s %s, %d day(s) overdue)",
					rental.ID, rental.Car.Brand, rental.Car.Model, overdueDays),
				ReferenceType: "rental", ReferenceID: rental.ID, Date: now,
			}); err != nil {
				return err
			}
		}
		return tx.Create(&models.RentalEvent{
			RentalID: rental.ID, FromStatus: "active", ToStatus: "returned", Note: note,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return GetRental(id)
}

// CancelRental cancels a booking that has not been paid.
//
// A rental with a live Xendit invoice must have that invoice expired first,
// otherwise the customer could still pay for a cancelled booking.
func CancelRental(id uint) (*models.Rental, error) {
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var rental models.Rental
		if err := tx.First(&rental, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NotFound("rental not found")
			}
			return err
		}
		if rental.Status != "pending" || rental.PaymentStatus == "paid" {
			return Conflict("only an unpaid, pending booking can be cancelled")
		}
		if rental.PaymentStatus == "pending" && !xendit.NewClient().Mock {
			return Conflict("expire the live payment invoice in Xendit before cancelling this booking")
		}

		previous := rental.Status
		rental.Status = "cancelled"
		if rental.PaymentStatus == "pending" {
			rental.PaymentStatus = "expired"
		}
		if err := tx.Save(&rental).Error; err != nil {
			return err
		}
		if rental.PaymentStatus == "expired" {
			if err := tx.Model(&models.Car{}).
				Where("id = ? AND status = ?", rental.CarID, "reserved").
				Update("status", "available").Error; err != nil {
				return err
			}
		}
		return tx.Create(&models.RentalEvent{
			RentalID: rental.ID, FromStatus: previous, ToStatus: "cancelled",
			Note: "Booking cancelled by the administrator",
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return GetRental(id)
}

// Tracking lists the cars currently out on the road with overdue detection.
func Tracking() ([]TrackingEntry, error) {
	var rentals []models.Rental
	if err := config.DB.Preload("Car").Preload("Customer").
		Where("status = ?", "active").
		Order("end_date asc").
		Find(&rentals).Error; err != nil {
		return nil, err
	}

	now := time.Now()
	entries := make([]TrackingEntry, 0, len(rentals))
	for _, rental := range rentals {
		overdue := now.After(rental.EndDate)
		daysLeft := int(math.Ceil(rental.EndDate.Sub(now).Hours() / 24))
		overdueDays := 0
		if overdue {
			overdueDays = int(math.Ceil(now.Sub(rental.EndDate).Hours() / 24))
			daysLeft = 0
		}
		entries = append(entries, TrackingEntry{
			ID: rental.ID, Car: rental.Car, Customer: rental.Customer,
			DriverName: rental.DriverName, DriverLicense: rental.DriverLicense,
			DriverPhone: rental.DriverPhone, BorrowedAt: rental.BorrowedAt,
			DueDate: rental.EndDate, OdometerOut: rental.OdometerOut,
			Overdue: overdue, DaysLeft: daysLeft, OverdueDays: overdueDays,
		})
	}
	return entries, nil
}

// ExportRentalsCSV renders the filtered rental list as CSV.
func ExportRentalsCSV(filter RentalFilter) (string, string, error) {
	filter.Paged = false
	raw, err := ListRentals(filter)
	if err != nil {
		return "", "", err
	}
	rentals, ok := raw.([]models.Rental)
	if !ok {
		return "", "", fmt.Errorf("unexpected rental type")
	}

	var builder strings.Builder
	builder.WriteString("Rental ID,Status,Payment,Car,Plate,Customer,Driver,Pick-up,Return,Days,Total,Late fee\n")
	for _, rental := range rentals {
		fmt.Fprintf(&builder, "%d,%s,%s,%s,%s,%s,%s,%s,%s,%d,%.2f,%.2f\n",
			rental.ID,
			csvCell(rental.Status),
			csvCell(rental.PaymentStatus),
			csvCell(rental.Car.Brand+" "+rental.Car.Model),
			csvCell(rental.Car.Plate),
			csvCell(rental.Customer.Name),
			csvCell(rental.DriverName),
			rental.StartDate.Format("2006-01-02"),
			rental.EndDate.Format("2006-01-02"),
			daysBetween(rental.StartDate, rental.EndDate),
			rental.TotalPrice,
			rental.LateFee,
		)
	}
	return builder.String(), "rentals.csv", nil
}
