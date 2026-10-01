package handlers

import (
	"log"

	"github.com/gofiber/fiber/v3"

	"car-rental-backend/services"
)

// RespondSuccess sends the standard success envelope.
func RespondSuccess(c fiber.Ctx, status int, data any, message string) error {
	return c.Status(status).JSON(fiber.Map{
		"success": true,
		"data":    data,
		"message": message,
	})
}

// respondSuccess is the single success path used by every controller, so the
// envelope is identical everywhere.
func respondSuccess(c fiber.Ctx, status int, data any, message string) error {
	return RespondSuccess(c, status, data, message)
}

// pathID reads the `:id` route parameter and converts it to a primary key.
// A malformed segment is rejected as a 400 before it reaches the data layer;
// callers pass the error straight to respondError like any other failure.
func pathID(c fiber.Ctx) (uint, error) {
	return services.ParseID(c.Params("id"))
}

// respondError translates a domain error into the standard failure envelope.
//
// Domain errors carry a message that is safe to show the user. Anything else
// is logged server-side and replaced with a generic message so internal
// details (SQL, stack traces) never reach the browser.
func respondError(c fiber.Ctx, err error) error {
	status := services.StatusCode(err)
	message := services.PublicMessage(err)

	if status >= 500 {
		log.Printf("%s %s -> %d: %v", c.Method(), c.Path(), status, err)
	}

	return c.Status(status).JSON(fiber.Map{
		"success": false,
		"data":    nil,
		"message": message,
	})
}

// respondInvalid is shorthand for the most common controller-level failure.
func respondInvalid(c fiber.Ctx, message string) error {
	return respondError(c, services.Invalid(message))
}
