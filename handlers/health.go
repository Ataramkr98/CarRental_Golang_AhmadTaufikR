package handlers

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"car-rental-backend/config"
	"car-rental-backend/services"
)

// Health reports service liveness plus database reachability, which is what
// container orchestrators and uptime checks actually need to know.
func Health(c fiber.Ctx) error {
	database := "connected"
	status := fiber.StatusOK

	if config.DB == nil {
		database = "not connected"
		status = fiber.StatusServiceUnavailable
	} else if sqlDB, err := config.DB.DB(); err != nil {
		database = "unavailable"
		status = fiber.StatusServiceUnavailable
	} else if err := sqlDB.Ping(); err != nil {
		database = "unreachable"
		status = fiber.StatusServiceUnavailable
	}

	if status != fiber.StatusOK {
		// Failures keep `data: null` like every other error response, so a
		// client can rely on `success === false` always meaning "no payload".
		// The detail is carried in the message, which is what health checks read.
		return respondError(c, services.Unavailable("service health degraded: database is "+database))
	}

	return RespondSuccess(c, status, fiber.Map{
		"status":     "ok",
		"database":   database,
		"checked_at": time.Now().UTC().Format(time.RFC3339),
	}, "Service health")
}
