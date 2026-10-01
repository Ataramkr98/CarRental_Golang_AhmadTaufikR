package services

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"car-rental-backend/config"
	"car-rental-backend/models"
)

// CarStatuses enumerates the lifecycle states a fleet vehicle can be in.
var CarStatuses = map[string]bool{
	"available":   true,
	"reserved":    true,
	"rented":      true,
	"maintenance": true,
}

// CarFilter narrows a fleet listing.
type CarFilter struct {
	Status string
	Search string
	Sort   string
	Page   Page
	Paged  bool
}

// CarInput is the writable shape of a car.
type CarInput struct {
	Brand      string  `json:"brand"`
	Model      string  `json:"model"`
	Year       int     `json:"year"`
	Plate      string  `json:"plate"`
	DailyPrice float64 `json:"daily_price"`
	Status     string  `json:"status"`
	Image      string  `json:"image"`
}

// CarPatch is a partial update; nil fields are left untouched.
type CarPatch struct {
	Brand      *string  `json:"brand"`
	Model      *string  `json:"model"`
	Year       *int     `json:"year"`
	Plate      *string  `json:"plate"`
	DailyPrice *float64 `json:"daily_price"`
	Status     *string  `json:"status"`
	Image      *string  `json:"image"`
}

// PublicCars lists only vehicles that can be booked right now.
func PublicCars() ([]models.Car, error) {
	var cars []models.Car
	err := config.DB.Where("status = ?", "available").Order("daily_price asc, id asc").Find(&cars).Error
	if err != nil {
		return nil, err
	}
	return cars, nil
}

// ListCars returns the fleet for the admin console.
func ListCars(filter CarFilter) (any, error) {
	query := config.DB.Model(&models.Car{})

	if status := normalizeStatus(filter.Status); status != "" {
		if !CarStatuses[status] {
			return nil, Invalid("invalid car status")
		}
		query = query.Where("status = ?", status)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(brand) LIKE ? OR LOWER(model) LIKE ? OR LOWER(plate) LIKE ?", like, like, like)
	}

	if !filter.Paged {
		var cars []models.Car
		if err := applyCarSort(query, filter.Sort).Find(&cars).Error; err != nil {
			return nil, err
		}
		return cars, nil
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var cars []models.Car
	if err := filter.Page.Apply(applyCarSort(query, filter.Sort)).Find(&cars).Error; err != nil {
		return nil, err
	}
	return NewPageResult(cars, total, filter.Page), nil
}

func applyCarSort(query *gorm.DB, sort string) *gorm.DB {
	switch strings.TrimSpace(sort) {
	case "price_desc":
		return query.Order("daily_price desc, id asc")
	case "price_asc":
		return query.Order("daily_price asc, id asc")
	case "year_desc":
		return query.Order("year desc, id asc")
	case "brand_asc":
		return query.Order("brand asc, model asc")
	default:
		return query.Order("id asc")
	}
}

// GetCar loads one vehicle by id.
func GetCar(id uint) (*models.Car, error) {
	var car models.Car
	if err := config.DB.First(&car, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("car not found")
		}
		return nil, err
	}
	return &car, nil
}

func normalizeCar(car *models.Car) {
	car.Brand = strings.TrimSpace(car.Brand)
	car.Model = strings.TrimSpace(car.Model)
	car.Plate = strings.ToUpper(strings.TrimSpace(car.Plate))
	car.Status = normalizeStatus(car.Status)
	car.Image = strings.TrimSpace(car.Image)
}

func validateCar(car models.Car) error {
	if car.Brand == "" || car.Model == "" || car.Plate == "" {
		return Invalid("brand, model and plate are required")
	}
	if err := validateTextLengths(map[string]string{
		"brand": car.Brand, "model": car.Model, "plate": car.Plate, "image url": car.Image,
	}); err != nil {
		return err
	}
	if car.Year < 1886 || car.Year > time.Now().Year()+1 {
		return Invalid("year is out of range")
	}
	if !isFinitePositive(car.DailyPrice) {
		return Invalid("daily price must be greater than zero")
	}
	if !CarStatuses[car.Status] {
		return Invalid("invalid car status")
	}
	return nil
}

// translateWriteError turns a unique-constraint violation into a 409.
//
// GORM's TranslateError option normalises the driver's error into
// gorm.ErrDuplicatedKey, so this no longer depends on matching message text.
func translateWriteError(err error, conflictMessage string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return Conflict(conflictMessage)
	}
	return err
}

// CreateCar validates and stores a new vehicle.
func CreateCar(in CarInput) (*models.Car, error) {
	car := models.Car{
		Brand: in.Brand, Model: in.Model, Year: in.Year, Plate: in.Plate,
		DailyPrice: in.DailyPrice, Status: in.Status, Image: in.Image,
	}
	if strings.TrimSpace(car.Status) == "" {
		car.Status = "available"
	}
	normalizeCar(&car)
	if err := validateCar(car); err != nil {
		return nil, err
	}
	if err := config.DB.Create(&car).Error; err != nil {
		return nil, translateWriteError(err, "a car with this plate already exists")
	}
	return &car, nil
}

// UpdateCar applies a whitelisted partial update.
//
// Status changes are guarded: a vehicle with a live rental must keep the
// status that rental depends on, otherwise tracking and reservation logic
// would silently disagree with the fleet view.
func UpdateCar(id uint, patch CarPatch) (*models.Car, error) {
	car, err := GetCar(id)
	if err != nil {
		return nil, err
	}

	next := *car
	if patch.Brand != nil {
		next.Brand = *patch.Brand
	}
	if patch.Model != nil {
		next.Model = *patch.Model
	}
	if patch.Year != nil {
		next.Year = *patch.Year
	}
	if patch.Plate != nil {
		next.Plate = *patch.Plate
	}
	if patch.DailyPrice != nil {
		next.DailyPrice = *patch.DailyPrice
	}
	if patch.Status != nil {
		next.Status = *patch.Status
	}
	if patch.Image != nil {
		next.Image = *patch.Image
	}
	normalizeCar(&next)
	if err := validateCar(next); err != nil {
		return nil, err
	}

	if next.Status != car.Status {
		if err := guardCarStatusChange(car.ID, next.Status); err != nil {
			return nil, err
		}
	}

	err = config.DB.Model(car).
		Select("Brand", "Model", "Year", "Plate", "DailyPrice", "Status", "Image").
		Updates(&next).Error
	if err != nil {
		return nil, translateWriteError(err, "a car with this plate already exists")
	}
	return &next, nil
}

// guardCarStatusChange rejects status edits that contradict live rentals.
func guardCarStatusChange(carID uint, nextStatus string) error {
	var active int64
	if err := config.DB.Model(&models.Rental{}).
		Where("car_id = ? AND status = ?", carID, "active").Count(&active).Error; err != nil {
		return err
	}
	var reserved int64
	if err := config.DB.Model(&models.Rental{}).
		Where("car_id = ? AND (status = ? OR (status = ? AND payment_status = ?))", carID, "paid", "pending", "pending").
		Count(&reserved).Error; err != nil {
		return err
	}
	if active > 0 && nextStatus != "rented" {
		return Conflict("an active rental requires this car to stay marked as rented")
	}
	if reserved > 0 && nextStatus != "reserved" {
		return Conflict("a paid or invoiced rental requires this car to stay marked as reserved")
	}
	// The reverse direction matters just as much: a car cannot claim to be on
	// the road, or reserved for a booking, without a rental behind it. Without
	// this the fleet table and the tracking board silently disagree.
	if nextStatus == "rented" && active == 0 {
		return Conflict("this car has no active rental, so it cannot be marked as rented")
	}
	if nextStatus == "reserved" && reserved == 0 {
		return Conflict("this car has no paid or invoiced booking, so it cannot be marked as reserved")
	}
	return nil
}

// DeleteCar removes a vehicle only when it has no rental history, so the
// audit trail and financial ledger stay intact.
func DeleteCar(id uint) error {
	car, err := GetCar(id)
	if err != nil {
		return err
	}
	var rentals int64
	if err := config.DB.Model(&models.Rental{}).Where("car_id = ?", car.ID).Count(&rentals).Error; err != nil {
		return err
	}
	if rentals > 0 {
		return Conflict("this car has rental history and cannot be deleted")
	}
	return config.DB.Delete(car).Error
}
