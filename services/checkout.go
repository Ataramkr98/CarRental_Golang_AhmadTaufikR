package services

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"car-rental-backend/config"
	"car-rental-backend/models"
)

// checkoutTokenTTL bounds how long a guest checkout session stays usable. It is
// long enough for a slow payment redirect and short enough that a leaked link
// is worthless a day later.
const checkoutTokenTTL = 24 * time.Hour

// IssueCheckoutToken mints a fresh guest session for a rental inside an
// existing transaction and returns the raw token. Only the hash is persisted.
func IssueCheckoutToken(tx *gorm.DB, rental *models.Rental) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	expiry := time.Now().Add(checkoutTokenTTL)

	rental.CheckoutTokenHash = hashCheckoutToken(token)
	rental.CheckoutTokenExpiry = &expiry
	if err := tx.Model(&models.Rental{}).Where("id = ?", rental.ID).
		Updates(map[string]any{
			"checkout_token_hash":   rental.CheckoutTokenHash,
			"checkout_token_expiry": expiry,
		}).Error; err != nil {
		return "", err
	}
	return token, nil
}

// hashCheckoutToken is the one-way digest used for both storage and lookup.
func hashCheckoutToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// VerifyCheckoutToken authorises a guest against a single rental.
//
// The console does not use this path: an authenticated staff member is already
// trusted by the route middleware, and the console calls the same endpoints with
// its bearer token.
func VerifyCheckoutToken(rentalID uint, provided string) error {
	provided = strings.TrimSpace(provided)
	if provided == "" || rentalID == 0 {
		return Unauthorized("this booking session is not valid — please book again")
	}

	var rental models.Rental
	if err := config.DB.
		Select("id", "checkout_token_hash", "checkout_token_expiry").
		First(&rental, rentalID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NotFound("rental not found")
		}
		return err
	}

	if rental.CheckoutTokenHash == "" || rental.CheckoutTokenExpiry == nil {
		return Unauthorized("this booking session is not valid — please book again")
	}
	if time.Now().After(*rental.CheckoutTokenExpiry) {
		return Unauthorized("this booking session has expired — please book again")
	}
	if subtle.ConstantTimeCompare([]byte(hashCheckoutToken(provided)), []byte(rental.CheckoutTokenHash)) != 1 {
		return Unauthorized("this booking session is not valid — please book again")
	}
	return nil
}
