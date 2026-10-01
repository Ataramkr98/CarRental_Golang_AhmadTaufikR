package services

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"car-rental-backend/config"
	"car-rental-backend/models"
)

// maxPasswordLength guards bcrypt, which silently truncates anything beyond 72
// bytes. Rejecting a longer input is safer than accepting a password whose tail
// is ignored.
const maxPasswordLength = 72

// validatePassword enforces the shared password policy for every path that sets
// one: registration, reset, and the console password change.
func validatePassword(password string) error {
	switch {
	case len(password) < 8:
		return Invalid("password must be at least 8 characters")
	case len(password) > maxPasswordLength:
		return Invalid("password must be at most 72 characters")
	default:
		return nil
	}
}

// Session is the result of a successful login.
type Session struct {
	Token     string      `json:"token"`
	ExpiresAt time.Time   `json:"expires_at"`
	User      models.User `json:"user"`
}

// tokenTTL is how long an issued admin token stays valid.
const tokenTTL = 24 * time.Hour

// Authenticate verifies an admin's credentials and issues a signed JWT.
// Both an unknown email and a wrong password return the same error so the
// endpoint cannot be used to enumerate accounts.
func Authenticate(email, password string) (*Session, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil, Invalid("email and password are required")
	}

	var user models.User
	if err := config.DB.Where("email = ?", email).First(&user).Error; err != nil {
		return nil, Unauthorized("invalid email or password")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return nil, Unauthorized("invalid email or password")
	}

	expiresAt := time.Now().Add(tokenTTL)
	claims := jwt.MapClaims{
		"sub":   user.ID,
		"email": user.Email,
		"role":  user.Role,
		"exp":   expiresAt.Unix(),
		"iat":   time.Now().Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(config.JWTSecret())
	if err != nil {
		return nil, err
	}

	return &Session{Token: signed, ExpiresAt: expiresAt, User: user}, nil
}

// CurrentUser loads the admin identified by a user id from the token subject.
func CurrentUser(userID uint) (*models.User, error) {
	if userID == 0 {
		return nil, Unauthorized("invalid session")
	}
	var user models.User
	if err := config.DB.First(&user, userID).Error; err != nil {
		return nil, NotFound("account not found")
	}
	return &user, nil
}

// UpdateProfile changes the admin's display name and email address.
func UpdateProfile(userID uint, name, email string) (*models.User, error) {
	user, err := CurrentUser(userID)
	if err != nil {
		return nil, err
	}

	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	if name == "" {
		return nil, Invalid("name is required")
	}
	if err := limitField("name", name, maxShortText); err != nil {
		return nil, err
	}
	if !validEmail(email) {
		return nil, Invalid("a valid email address is required")
	}

	if email != user.Email {
		var taken int64
		if err := config.DB.Model(&models.User{}).
			Where("email = ? AND id <> ?", email, user.ID).Count(&taken).Error; err != nil {
			return nil, err
		}
		if taken > 0 {
			return nil, Conflict("that email address is already in use")
		}
	}

	user.Name = name
	user.Email = email
	if err := config.DB.Model(user).Updates(map[string]any{"name": name, "email": email}).Error; err != nil {
		return nil, err
	}
	return user, nil
}

// ChangePassword rotates the admin's password after verifying the current one.
func ChangePassword(userID uint, currentPassword, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if newPassword == currentPassword {
		return Invalid("new password must be different from the current one")
	}

	user, err := CurrentUser(userID)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(currentPassword)); err != nil {
		return Unauthorized("current password is incorrect")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return config.DB.Model(user).Update("password", string(hash)).Error
}

// ---- registration ----

// RegistrationEnabled reports whether self-service sign-up is on. It is off by
// default so an unconfigured production deployment never opens account
// creation; the demo environment turns it on explicitly.
func RegistrationEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_REGISTRATION")), "true")
}

// Register creates a new console account with the least-privileged role and
// returns a session, so the UI can sign the user straight in.
func Register(name, email, password string) (*Session, error) {
	if !RegistrationEnabled() {
		return nil, Forbidden("registration is disabled on this deployment")
	}

	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	if name == "" {
		return nil, Invalid("name is required")
	}
	if err := limitField("name", name, maxShortText); err != nil {
		return nil, err
	}
	if !validEmail(email) {
		return nil, Invalid("a valid email address is required")
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}

	var existing int64
	if err := config.DB.Model(&models.User{}).Where("email = ?", email).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, Conflict("an account with that email already exists")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	user := models.User{
		Name:     name,
		Email:    email,
		Password: string(hash),
		Role:     "operator", // self-registered accounts never receive admin rights
	}
	if err := config.DB.Create(&user).Error; err != nil {
		return nil, err
	}
	return Authenticate(email, password)
}

// ---- password reset ----

// resetTokenTTL is how long a reset link stays valid.
const resetTokenTTL = 30 * time.Minute

// PasswordResetResult is the response of a forgot-password request.
//
// In demo mode (no SMTP configured) the reset URL is returned directly so the
// flow is completable without an e-mail provider — the same trade-off the mock
// payment gateway makes. In live mode the URL is only ever e-mailed.
type PasswordResetResult struct {
	Sent     bool   `json:"sent"`
	Demo     bool   `json:"demo"`
	ResetURL string `json:"reset_url,omitempty"`
}

// newResetToken returns a 256-bit random token as a hex string.
func newResetToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// hashResetToken is the one-way digest stored in the database, so a leaked
// database dump cannot be replayed as working reset links.
func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RequestPasswordReset issues a reset token for the given address.
//
// The response is identical whether or not the account exists, except in demo
// mode where the link itself must be shown on screen (documented in the README
// as a demo-only trade-off). With SMTP configured the response never contains
// a token, so account enumeration is not possible from the body.
func RequestPasswordReset(email string) (*PasswordResetResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) {
		return nil, Invalid("a valid email address is required")
	}

	result := &PasswordResetResult{Sent: true}

	var user models.User
	if err := config.DB.Where("email = ?", email).First(&user).Error; err != nil {
		// Unknown address: succeed silently with no token, matching the shape
		// of a live-mode response so the caller cannot tell the difference.
		return result, nil
	}

	token, err := newResetToken()
	if err != nil {
		return nil, err
	}
	expiry := time.Now().Add(resetTokenTTL)
	if err := config.DB.Model(&user).Updates(map[string]any{
		"reset_token_hash":   hashResetToken(token),
		"reset_token_expiry": &expiry,
	}).Error; err != nil {
		return nil, err
	}

	resetURL := AppURL() + "/reset-password?token=" + token

	if smtpConfigured() {
		if err := sendPasswordResetEmail(user.Email, resetURL); err != nil {
			// The transport error is the only diagnostic for a mail outage, so
			// it is logged before the caller receives a generic 502.
			log.Printf("smtp: password reset e-mail to %s failed: %v", user.Email, err)
			return nil, BadGateway("could not send the reset e-mail, please try again later")
		}
		return result, nil
	}

	// Demo mode: no mail server, so hand the link back for on-screen display.
	result.Demo = true
	result.ResetURL = resetURL
	return result, nil
}

// ResetPassword consumes a reset token and sets the new password. Tokens are
// single-use: a successful reset clears them, and an expired or unknown token
// fails with the same generic message.
func ResetPassword(token, newPassword string) error {
	if token == "" {
		return Invalid("this reset link is invalid or expired")
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}

	var user models.User
	if err := config.DB.Where("reset_token_hash = ?", hashResetToken(token)).First(&user).Error; err != nil {
		return Invalid("this reset link is invalid or expired")
	}
	if user.ResetTokenExpiry == nil || time.Now().After(*user.ResetTokenExpiry) {
		return Invalid("this reset link is invalid or expired")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return config.DB.Model(&user).Updates(map[string]any{
		"password":           string(hash),
		"reset_token_hash":   "",
		"reset_token_expiry": nil,
	}).Error
}

// AppURL is the public origin of the SPA, used to build absolute links such
// as password-reset URLs.
func AppURL() string {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("APP_URL")), "/"); value != "" {
		return value
	}
	return "http://localhost:5173"
}
