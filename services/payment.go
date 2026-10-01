package services

import (
	"crypto/subtle"
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

// InvoiceResult is what the checkout UI needs to render a payment step.
type InvoiceResult struct {
	InvoiceID     string  `json:"invoice_id"`
	InvoiceURL    string  `json:"invoice_url"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	PaymentStatus string  `json:"payment_status"`
	Mock          bool    `json:"mock"`
	Reused        bool    `json:"reused,omitempty"`
	AlreadyPaid   bool    `json:"already_paid,omitempty"`
	Message       string  `json:"message,omitempty"`
}

// RentalStatusView is the sanitised lifecycle snapshot served to the public
// checkout page. It intentionally exposes no customer or driver data.
type RentalStatusView struct {
	ID            uint       `json:"id"`
	Status        string     `json:"status"`
	PaymentStatus string     `json:"payment_status"`
	PaidAt        *time.Time `json:"paid_at"`
}

// MockPaymentsEnabled reports whether the gateway is running in mock mode.
func MockPaymentsEnabled() bool { return xendit.NewClient().Mock }

// configuredCurrency resolves the currency used for prices and invoices.
func configuredCurrency() string {
	if currency := strings.ToUpper(strings.TrimSpace(os.Getenv("CURRENCY"))); currency != "" {
		return currency
	}
	return "USD"
}

// GetRentalStatus returns the public, sanitised payment status of a rental.
func GetRentalStatus(id uint) (*RentalStatusView, error) {
	var rental models.Rental
	err := config.DB.Select("id", "status", "payment_status", "paid_at").
		First(&rental, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("rental not found")
		}
		return nil, err
	}
	return &RentalStatusView{
		ID: rental.ID, Status: rental.Status,
		PaymentStatus: rental.PaymentStatus, PaidAt: rental.PaidAt,
	}, nil
}

// CreateInvoice reserves the car and creates — or safely reuses — a single
// Xendit invoice for a pending rental. Calling it twice never double-charges.
func CreateInvoice(rentalID uint) (*InvoiceResult, error) {
	client := xendit.NewClient()
	currency := configuredCurrency()
	var (
		rental      models.Rental
		carReserved bool
		earlyResult *InvoiceResult
	)

	// Step 1: Validate rental state, reuse existing invoice if present, and reserve car
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Preload("Car").Preload("Customer").First(&rental, rentalID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NotFound("rental not found")
			}
			return err
		}

		if rental.PaymentStatus == "paid" {
			earlyResult = &InvoiceResult{
				InvoiceURL: rental.InvoiceURL, Amount: rental.TotalPrice,
				Currency: currency, PaymentStatus: "paid", Mock: client.Mock,
				AlreadyPaid: true, Message: "This booking is already paid",
			}
			return nil
		}
		if rental.Status != "pending" {
			return Conflict("only a pending booking can be paid")
		}
		if rental.PaymentStatus == "pending" && rental.InvoiceID != "" {
			earlyResult = &InvoiceResult{
				InvoiceID: rental.InvoiceID, InvoiceURL: rental.InvoiceURL,
				Amount: rental.TotalPrice, Currency: currency, Mock: client.Mock,
				PaymentStatus: rental.PaymentStatus, Reused: true,
			}
			return nil
		}

		if err := assertCarReservable(tx, rental.CarID, rental.ID); err != nil {
			return err
		}
		if err := reserveCar(tx, &rental); err != nil {
			return err
		}
		if rental.Car.Status == "available" {
			carReserved = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if earlyResult != nil {
		return earlyResult, nil
	}

	// Step 2: Make external network call OUTSIDE of any DB transaction
	externalID := "rental-" + strconv.FormatUint(uint64(rental.ID), 10)
	invoice, err := client.CreateInvoice(xendit.CreateInvoiceRequest{
		ExternalID: externalID, Amount: rental.TotalPrice, Currency: currency,
		Description:   fmt.Sprintf("Car rental #%d — %s %s", rental.ID, rental.Car.Brand, rental.Car.Model),
		CustomerEmail: rental.Customer.Email,
	})
	if err != nil {
		if carReserved {
			_ = config.DB.Model(&models.Car{}).Where("id = ? AND status = ?", rental.CarID, "reserved").Update("status", "available")
		}
		return nil, BadGateway("the payment gateway is unavailable, please try again")
	}

	// Step 3: Record invoice details and audit event
	err = config.DB.Transaction(func(tx *gorm.DB) error {
		rental.InvoiceID = invoice.ID
		rental.InvoiceURL = invoice.InvoiceURL
		rental.PaymentStatus = "pending"
		rental.PaymentMethod = "Xendit Invoice"
		if err := tx.Save(&rental).Error; err != nil {
			return err
		}
		return tx.Create(&models.RentalEvent{
			RentalID: rental.ID, FromStatus: rental.Status, ToStatus: rental.Status,
			Note: "Payment invoice created (" + invoice.ID + ")",
		}).Error
	})
	if err != nil {
		return nil, err
	}

	return &InvoiceResult{
		InvoiceID: invoice.ID, InvoiceURL: invoice.InvoiceURL,
		Amount: rental.TotalPrice, Currency: currency, Mock: client.Mock,
		PaymentStatus: rental.PaymentStatus,
	}, nil
}

// assertCarReservable checks no other live rental already holds the car.
func assertCarReservable(tx *gorm.DB, carID, rentalID uint) error {
	var conflicts int64
	if err := tx.Model(&models.Rental{}).
		Where("car_id = ? AND id <> ? AND (status IN ? OR (status = ? AND payment_status = ?))",
			carID, rentalID, []string{"paid", "active"}, "pending", "pending").
		Count(&conflicts).Error; err != nil {
		return err
	}
	if conflicts > 0 {
		return Conflict("this car has already been reserved by another booking")
	}
	return nil
}

// reserveCar atomically moves the car into the reserved state.
func reserveCar(tx *gorm.DB, rental *models.Rental) error {
	switch rental.Car.Status {
	case "available":
		outcome := tx.Model(&models.Car{}).
			Where("id = ? AND status = ?", rental.CarID, "available").
			Update("status", "reserved")
		if outcome.Error != nil {
			return outcome.Error
		}
		if outcome.RowsAffected != 1 {
			return Conflict("this car is no longer available")
		}
		return nil
	case "reserved":
		// The invoice creation path already reserved it for this rental.
		return nil
	default:
		return Conflict("this car is no longer available")
	}
}

// HandleWebhook processes a Xendit invoice callback after the caller has
// verified the callback token.
func HandleWebhook(externalID, status string, paidAmount float64) error {
	if !strings.HasPrefix(externalID, "rental-") {
		return Invalid("unknown invoice reference")
	}

	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PAID", "SETTLED":
		// The amount is always verified, in mock mode as well as live: it is
		// the only check that a callback belongs to this booking's total.
		if paidAmount <= 0 {
			return Invalid("paid_amount is required")
		}
		return MarkExternalRentalPaid(externalID, paidAmount)
	case "EXPIRED":
		return MarkRentalExpired(externalID)
	default:
		// Unknown statuses are acknowledged but ignored, so Xendit does not
		// retry callbacks this service deliberately does not act on.
		return nil
	}
}

// VerifyCallbackToken compares the webhook header against the configured token.
//
// It fails closed in every mode: an unconfigured token is a 503 rather than an
// open endpoint, because a webhook that can mark invoices paid is a direct
// write path into the cash ledger. Xendit is instructed to echo the token back
// on every callback, so this costs nothing in live mode.
func VerifyCallbackToken(provided string) error {
	expected := strings.TrimSpace(os.Getenv("XENDIT_CALLBACK_TOKEN"))
	if expected == "" {
		return Unavailable("the payment callback token is not configured")
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(provided)), []byte(expected)) == 1 {
		return nil
	}
	return Unauthorized("invalid callback token")
}

// MarkRentalPaid advances a paid rental, reserves its car and records the
// income. Repeat callbacks are idempotent.
func MarkRentalPaid(rentalID uint, paidAmount float64) error {
	if rentalID == 0 {
		return Invalid("invalid rental id")
	}
	return markRentalPaid(rentalID, paidAmount)
}

// MarkExternalRentalPaid resolves a `rental-<id>` external id from a payment
// gateway callback and applies the same transition as MarkRentalPaid.
func MarkExternalRentalPaid(externalID string, paidAmount float64) error {
	id, err := parseRentalID(externalID)
	if err != nil {
		return err
	}
	return markRentalPaid(id, paidAmount)
}

func markRentalPaid(id uint, paidAmount float64) error {
	return config.DB.Transaction(func(tx *gorm.DB) error {
		var rental models.Rental
		if err := tx.Preload("Car").First(&rental, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NotFound("rental not found")
			}
			return err
		}
		if rental.PaymentStatus == "paid" {
			return nil // idempotent: a repeated callback is a no-op
		}
		if rental.Status != "pending" {
			return Conflict("payment cannot be applied to this booking")
		}
		if paidAmount > 0 && math.Abs(paidAmount-rental.TotalPrice) > 0.01 {
			return Invalid("the paid amount does not match the booking total")
		}
		if err := assertCarReservable(tx, rental.CarID, rental.ID); err != nil {
			return err
		}
		if err := reserveCar(tx, &rental); err != nil {
			return err
		}

		now := time.Now()
		previous := rental.Status
		rental.PaymentStatus = "paid"
		rental.PaidAt = &now
		rental.Status = "paid"
		if rental.PaymentMethod == "" {
			rental.PaymentMethod = "Mock payment"
		}
		if err := tx.Save(&rental).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.RentalEvent{
			RentalID: rental.ID, FromStatus: previous, ToStatus: "paid",
			Note: "Payment received",
		}).Error; err != nil {
			return err
		}
		return recordCash(tx, models.CashFlow{
			Type: "in", Category: "rental_income", Amount: rental.TotalPrice,
			Description:   fmt.Sprintf("Rental #%d — %s %s", rental.ID, rental.Car.Brand, rental.Car.Model),
			ReferenceType: "rental", ReferenceID: rental.ID, Date: now,
		})
	})
}

// MarkRentalExpired releases a reservation whose invoice timed out.
func MarkRentalExpired(externalID string) error {
	id, err := parseRentalID(externalID)
	if err != nil {
		return err
	}

	return config.DB.Transaction(func(tx *gorm.DB) error {
		var rental models.Rental
		if err := tx.First(&rental, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NotFound("rental not found")
			}
			return err
		}
		if rental.PaymentStatus == "expired" || rental.Status == "cancelled" {
			return nil // idempotent
		}
		if rental.Status != "pending" || rental.PaymentStatus != "pending" {
			return Conflict("this invoice cannot be expired in the current state")
		}

		rental.PaymentStatus = "expired"
		rental.InvoiceURL = ""
		if err := tx.Save(&rental).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Car{}).
			Where("id = ? AND status = ?", rental.CarID, "reserved").
			Update("status", "available").Error; err != nil {
			return err
		}
		return tx.Create(&models.RentalEvent{
			RentalID: rental.ID, FromStatus: "pending", ToStatus: "pending",
			Note: "Payment invoice expired — car released back to the fleet",
		}).Error
	})
}

// parseRentalID accepts either a bare id or a `rental-<id>` external id.
func parseRentalID(idOrExternal string) (uint, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(idOrExternal), "rental-")
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed == 0 {
		return 0, Invalid("invalid rental id")
	}
	return uint(parsed), nil
}
