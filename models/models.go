package models

import (
	"time"
)

// User is a console account. Two roles exist:
//
//	admin    — full access, including destructive record deletion
//	operator — day-to-day console access without delete privileges
//
// Only the SHA-256 hash of a password-reset token is stored; the raw token
// exists solely in the link handed to the user (or e-mailed in live mode).
type User struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Name             string     `json:"name"`
	Email            string     `gorm:"uniqueIndex" json:"email"`
	Password         string     `json:"-"` // never serialize the hash
	Role             string     `gorm:"default:operator;index" json:"role"`
	ResetTokenHash   string     `gorm:"index" json:"-"`
	ResetTokenExpiry *time.Time `json:"-"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// Car is a vehicle in the fleet.
type Car struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Brand      string    `gorm:"index" json:"brand"`
	Model      string    `json:"model"`
	Year       int       `json:"year"`
	Plate      string    `gorm:"uniqueIndex" json:"plate"`
	DailyPrice float64   `json:"daily_price"`
	Status     string    `gorm:"default:available;index" json:"status"` // available | reserved | rented | maintenance
	Image      string    `json:"image"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Customer is the person renting a car.
type Customer struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"index" json:"name"`
	Email     string    `gorm:"index" json:"email"`
	Phone     string    `json:"phone"`
	License   string    `json:"license"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Rental is a booking of one car by one customer.
//
// Lifecycle (library-style borrow flow):
//
//	pending  -> booking created, awaiting payment
//	paid     -> payment received (Xendit), car reserved, ready for pickup
//	active   -> car handed over; the customer is driving it (tracked)
//	returned -> car returned; late fee settled
//	cancelled-> booking cancelled
type Rental struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	CarID      uint      `gorm:"index" json:"car_id"`
	Car        Car       `json:"car"`
	CustomerID uint      `gorm:"index" json:"customer_id"`
	Customer   Customer  `json:"customer"`
	StartDate  time.Time `json:"start_date"`
	EndDate    time.Time `json:"end_date"` // due date (expected return)
	TotalPrice float64   `json:"total_price"`
	LateFee    float64   `json:"late_fee"`
	Status     string    `gorm:"default:pending;index" json:"status"` // pending | paid | active | returned | cancelled

	// ---- payment (Xendit) ----
	PaymentStatus string     `gorm:"default:unpaid;index" json:"payment_status"` // unpaid | pending | paid | expired
	PaymentMethod string     `json:"payment_method"`
	InvoiceID     string     `gorm:"index" json:"invoice_id"`
	InvoiceURL    string     `json:"invoice_url"`
	PaidAt        *time.Time `json:"paid_at"`

	// ---- guest checkout session ----
	// The storefront has no accounts, so a booking returns a single-use secret
	// that authorises its own pay / mock-pay / status calls. Only the SHA-256
	// hash is stored, exactly like a password-reset token, and the raw value is
	// never serialised on any read path.
	CheckoutTokenHash   string     `gorm:"index" json:"-"`
	CheckoutTokenExpiry *time.Time `json:"-"`

	// ---- library-style borrow / return tracking ----
	DriverName    string     `json:"driver_name"`
	DriverLicense string     `json:"driver_license"`
	DriverPhone   string     `json:"driver_phone"`
	OdometerOut   int        `json:"odometer_out"`
	OdometerIn    int        `json:"odometer_in"`
	PickupNote    string     `json:"pickup_note"`
	ReturnNote    string     `json:"return_note"`
	BorrowedAt    *time.Time `json:"borrowed_at"`
	ReturnedAt    *time.Time `json:"returned_at"`

	Events    []RentalEvent `json:"events"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// RentalEvent is one step in a rental's status history (the tracking timeline).
type RentalEvent struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	RentalID   uint      `gorm:"index" json:"rental_id"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
}

// CashFlow is one entry in the business cash ledger (kas masuk / keluar).
type CashFlow struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Type          string    `gorm:"index" json:"type"`     // in | out
	Category      string    `gorm:"index" json:"category"` // rental_income | late_fee | deposit | maintenance | fuel | salary | marketing | other
	Amount        float64   `json:"amount"`
	Description   string    `json:"description"`
	ReferenceType string    `gorm:"index" json:"reference_type"` // rental | manual
	ReferenceID   uint      `json:"reference_id"`
	Date          time.Time `gorm:"index" json:"date"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
