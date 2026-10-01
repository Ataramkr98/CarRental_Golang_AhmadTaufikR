package services

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"car-rental-backend/config"
	"car-rental-backend/models"
)

// CustomerFilter narrows a customer listing.
type CustomerFilter struct {
	Search string
	Sort   string
	Page   Page
	Paged  bool
}

// CustomerInput is the writable shape of a customer.
type CustomerInput struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone"`
	License string `json:"license"`
}

// CustomerPatch is a partial update; nil fields are left untouched.
type CustomerPatch struct {
	Name    *string `json:"name"`
	Email   *string `json:"email"`
	Phone   *string `json:"phone"`
	License *string `json:"license"`
}

// CustomerDetail bundles a customer with the history the admin UI shows.
type CustomerDetail struct {
	models.Customer
	Rentals []models.Rental `json:"rentals"`
	Stats   CustomerStats   `json:"stats"`
}

// CustomerStats summarises a customer's rental activity.
type CustomerStats struct {
	TotalRentals int64   `json:"total_rentals"`
	Active       int64   `json:"active"`
	TotalSpend   float64 `json:"total_spend"`
	Outstanding  int64   `json:"outstanding"`
}

// ListCustomers returns customers for the admin console.
func ListCustomers(filter CustomerFilter) (any, error) {
	query := config.DB.Model(&models.Customer{})

	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(email) LIKE ? OR LOWER(phone) LIKE ?", like, like, like)
	}

	if !filter.Paged {
		var customers []models.Customer
		if err := applyCustomerSort(query, filter.Sort).Find(&customers).Error; err != nil {
			return nil, err
		}
		return customers, nil
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var customers []models.Customer
	if err := filter.Page.Apply(applyCustomerSort(query, filter.Sort)).Find(&customers).Error; err != nil {
		return nil, err
	}
	return NewPageResult(customers, total, filter.Page), nil
}

func applyCustomerSort(query *gorm.DB, sort string) *gorm.DB {
	switch strings.TrimSpace(sort) {
	case "name_asc":
		return query.Order("name asc")
	case "oldest":
		return query.Order("id asc")
	default:
		return query.Order("id desc")
	}
}

// GetCustomer loads one customer by id.
func GetCustomer(id uint) (*models.Customer, error) {
	var customer models.Customer
	if err := config.DB.First(&customer, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("customer not found")
		}
		return nil, err
	}
	return &customer, nil
}

// GetCustomerDetail loads a customer together with their rental history.
func GetCustomerDetail(id uint) (*CustomerDetail, error) {
	customer, err := GetCustomer(id)
	if err != nil {
		return nil, err
	}

	var rentals []models.Rental
	if err := config.DB.Preload("Car").Preload("Customer").
		Where("customer_id = ?", customer.ID).
		Order("id desc").
		Find(&rentals).Error; err != nil {
		return nil, err
	}

	stats := CustomerStats{TotalRentals: int64(len(rentals))}
	for _, rental := range rentals {
		if rental.Status == "active" {
			stats.Active++
		}
		if rental.PaymentStatus == "paid" {
			stats.TotalSpend += rental.TotalPrice + rental.LateFee
		} else if rental.Status != "cancelled" {
			stats.Outstanding++
		}
	}

	return &CustomerDetail{Customer: *customer, Rentals: rentals, Stats: stats}, nil
}

// normalizeCustomer trims and lower-cases the mutable fields.
func normalizeCustomer(input *CustomerInput) {
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Phone = strings.TrimSpace(input.Phone)
	input.License = strings.TrimSpace(input.License)
}

// validateCustomer enforces the same rules for create and update.
func validateCustomer(customer models.Customer) error {
	if customer.Name == "" {
		return Invalid("name is required")
	}
	if err := validateTextLengths(map[string]string{
		"name": customer.Name, "email": customer.Email,
		"phone": customer.Phone, "licence": customer.License,
	}); err != nil {
		return err
	}
	if customer.Email == "" && customer.Phone == "" {
		return Invalid("an email address or phone number is required")
	}
	if customer.Email != "" && !validEmail(customer.Email) {
		return Invalid("a valid email address is required")
	}
	return nil
}

// CreateCustomer stores a new customer record.
func CreateCustomer(in CustomerInput) (*models.Customer, error) {
	normalizeCustomer(&in)
	customer := models.Customer{
		Name: in.Name, Email: in.Email, Phone: in.Phone, License: in.License,
	}
	if err := validateCustomer(customer); err != nil {
		return nil, err
	}
	if in.Email != "" {
		var existing int64
		if err := config.DB.Model(&models.Customer{}).Where("LOWER(email) = ?", in.Email).Count(&existing).Error; err != nil {
			return nil, err
		}
		if existing > 0 {
			return nil, Conflict("a customer with this email already exists")
		}
	}
	if err := config.DB.Create(&customer).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, Conflict("a customer with this email already exists")
		}
		return nil, err
	}
	return &customer, nil
}

// UpdateCustomer applies a partial update to an existing customer.
func UpdateCustomer(id uint, patch CustomerPatch) (*models.Customer, error) {
	customer, err := GetCustomer(id)
	if err != nil {
		return nil, err
	}

	next := *customer
	if patch.Name != nil {
		next.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Email != nil {
		next.Email = strings.ToLower(strings.TrimSpace(*patch.Email))
	}
	if patch.Phone != nil {
		next.Phone = strings.TrimSpace(*patch.Phone)
	}
	if patch.License != nil {
		next.License = strings.TrimSpace(*patch.License)
	}
	if err := validateCustomer(next); err != nil {
		return nil, err
	}

	if next.Email != "" && next.Email != customer.Email {
		var existing int64
		if err := config.DB.Model(&models.Customer{}).Where("LOWER(email) = ? AND id <> ?", next.Email, customer.ID).Count(&existing).Error; err != nil {
			return nil, err
		}
		if existing > 0 {
			return nil, Conflict("a customer with this email already exists")
		}
	}

	err = config.DB.Model(customer).
		Select("Name", "Email", "Phone", "License").
		Updates(&next).Error
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, Conflict("a customer with this email already exists")
		}
		return nil, err
	}
	return &next, nil
}

// DeleteCustomer removes a customer only when they have no rental history.
func DeleteCustomer(id uint) error {
	customer, err := GetCustomer(id)
	if err != nil {
		return err
	}
	var rentals int64
	if err := config.DB.Model(&models.Rental{}).Where("customer_id = ?", customer.ID).Count(&rentals).Error; err != nil {
		return err
	}
	if rentals > 0 {
		return Conflict("this customer has rental history and cannot be deleted")
	}
	return config.DB.Delete(customer).Error
}
