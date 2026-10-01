package middleware

import "github.com/gofiber/fiber/v3"

// SecurityHeaders sets conservative response headers on every request.
//
// The API only ever returns JSON, so a restrictive Content-Security-Policy
// and frame denial cost nothing and remove a class of browser-side attacks.
func SecurityHeaders() fiber.Handler {
	return func(c fiber.Ctx) error {
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("X-Frame-Options", "DENY")
		c.Set("Referrer-Policy", "no-referrer")
		c.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		c.Set("Cross-Origin-Resource-Policy", "same-site")
		if c.Protocol() == "https" || c.Get("X-Forwarded-Proto") == "https" {
			c.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		return c.Next()
	}
}
