package handlers

import (
	"github.com/gofiber/fiber/v3"

	"car-rental-backend/services"
)

// PublicConfig serves the runtime configuration the SPA reads at boot, so the
// currency and payment mode are defined only by the backend environment.
func PublicConfig(c fiber.Ctx) error {
	return respondSuccess(c, fiber.StatusOK, services.GetPublicConfig(), "Runtime configuration")
}
