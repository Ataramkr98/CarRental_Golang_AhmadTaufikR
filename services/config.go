package services

// PublicConfig is the runtime configuration the frontend needs in order to
// render consistently with the backend.
//
// It exists so that values like the currency are defined in exactly one place —
// the backend environment — instead of being duplicated as a frontend constant
// that silently drifts out of sync.
//
// It deliberately contains no secrets.
type PublicConfig struct {
	Currency          string  `json:"currency"`
	MockPayments      bool    `json:"mock_payments"`
	LateFeeMultiplier float64 `json:"late_fee_multiplier"`
	AllowRegistration bool    `json:"allow_registration"`
	PasswordResetDemo bool    `json:"password_reset_demo"`
}

// GetPublicConfig returns the client-visible runtime configuration.
func GetPublicConfig() PublicConfig {
	return PublicConfig{
		Currency:          configuredCurrency(),
		MockPayments:      MockPaymentsEnabled(),
		LateFeeMultiplier: lateFeeMultiplier(),
		AllowRegistration: RegistrationEnabled(),
		PasswordResetDemo: !smtpConfigured(),
	}
}
