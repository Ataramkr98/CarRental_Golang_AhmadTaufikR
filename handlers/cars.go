package handlers

import (
	"github.com/gofiber/fiber/v3"

	"car-rental-backend/services"
)

// PublicCars lists bookable vehicles. No authentication required.
func PublicCars(c fiber.Ctx) error {
	cars, err := services.PublicCars()
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, cars, "Cars retrieved successfully")
}

// ListCars lists the whole fleet for the admin console.
//
// Supports ?status=, ?search=, ?sort= and optional ?page=/?per_page=
// pagination; without `page` it returns a plain array.
func ListCars(c fiber.Ctx) error {
	page, paged, err := services.ParsePage(c, 20, 100)
	if err != nil {
		return respondError(c, err)
	}

	cars, err := services.ListCars(services.CarFilter{
		Status: c.Query("status"),
		Search: c.Query("search"),
		Sort:   c.Query("sort"),
		Page:   page,
		Paged:  paged,
	})
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, cars, "Cars retrieved successfully")
}

// GetCar returns a single vehicle.
func GetCar(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	car, err := services.GetCar(id)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, car, "Car retrieved successfully")
}

// CreateCar adds a validated vehicle to the fleet.
func CreateCar(c fiber.Ctx) error {
	var in services.CarInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	car, err := services.CreateCar(in)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusCreated, car, "Car added to the fleet")
}

// UpdateCar applies a whitelisted partial update.
func UpdateCar(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	var patch services.CarPatch
	if err := c.Bind().Body(&patch); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	car, err := services.UpdateCar(id, patch)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, car, "Car updated successfully")
}

// DeleteCar removes a vehicle that has no rental history.
func DeleteCar(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	if err := services.DeleteCar(id); err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, fiber.Map{"deleted": true}, "Car deleted successfully")
}
