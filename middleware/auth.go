package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"

	"car-rental-backend/config"
)

// unauthorized writes the standard failure envelope for a rejected request.
func unauthorized(c fiber.Ctx, message string, status int) error {
	return c.Status(status).JSON(fiber.Map{
		"success": false,
		"data":    nil,
		"message": message,
	})
}

// OptionalAuth parses a console JWT when one is present but never rejects the
// request.
//
// The guest checkout routes accept two credentials — a console session or the
// booking's own checkout token — so they run this first and let the handler
// decide which one applies. A malformed or expired token is simply treated as
// "no console session" here; those routes stay reachable for genuine guests.
func OptionalAuth() fiber.Handler {
	return func(c fiber.Ctx) error {
		parts := strings.Fields(c.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return c.Next()
		}
		if claims, ok := parseSession(parts[1]); ok {
			c.Locals("claims", claims)
		}
		return c.Next()
	}
}

// HasConsoleSession reports whether OptionalAuth accepted a valid console JWT
// on this request.
func HasConsoleSession(c fiber.Ctx) bool {
	_, ok := c.Locals("claims").(jwt.MapClaims)
	return ok
}

// parseSession validates a bearer token and returns its claims. It is shared
// by JWTAuth and OptionalAuth so both accept exactly the same tokens.
func parseSession(raw string) (jwt.MapClaims, bool) {
	token, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return config.JWTSecret(), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return nil, false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, false
	}
	role, _ := claims["role"].(string)
	if role != "admin" && role != "operator" {
		return nil, false
	}
	return claims, true
}

// JWTAuth validates the Bearer token on protected routes and enforces the
// admin role. It uses golang-jwt/jwt/v5 directly so it stays compatible with
// Fiber v3 without pulling in a contrib adapter.
func JWTAuth() fiber.Handler {
	return func(c fiber.Ctx) error {
		parts := strings.Fields(c.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return unauthorized(c, "authentication required", fiber.StatusUnauthorized)
		}

		claims, ok := parseSession(parts[1])
		if !ok {
			return unauthorized(c, "your session has expired, please sign in again", fiber.StatusUnauthorized)
		}

		c.Locals("claims", claims)
		return c.Next()
	}
}

// RequireAdmin restricts a route to the administrator role. Operators run the
// day-to-day console; irreversible actions such as deleting records are gated
// behind this middleware so the role difference is enforced server-side.
//
// Must be mounted after JWTAuth so the claims are already in Locals.
func RequireAdmin() fiber.Handler {
	return func(c fiber.Ctx) error {
		claims, ok := c.Locals("claims").(jwt.MapClaims)
		if !ok {
			return unauthorized(c, "authentication required", fiber.StatusUnauthorized)
		}
		if role, _ := claims["role"].(string); role != "admin" {
			return unauthorized(c, "administrator privileges are required for this action", fiber.StatusForbidden)
		}
		return c.Next()
	}
}

// UserID returns the authenticated admin's id from the token subject.
// JWT numbers decode as float64, so the value is converted back to uint.
func UserID(c fiber.Ctx) uint {
	claims, ok := c.Locals("claims").(jwt.MapClaims)
	if !ok {
		return 0
	}
	switch subject := claims["sub"].(type) {
	case float64:
		return uint(subject)
	case int:
		return uint(subject)
	case uint:
		return subject
	default:
		return 0
	}
}
