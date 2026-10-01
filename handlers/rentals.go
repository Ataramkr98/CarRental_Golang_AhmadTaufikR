package handlers

import (
	"github.com/gofiber/fiber/v3"

	"car-rental-backend/services"
)

// CreateBooking creates a pending booking from the public site.
//
// The response is sanitised — it never contains customer or driver details —
// and carries the one-time checkout token that authorises this booking's
// payment calls.
func CreateBooking(c fiber.Ctx) error {
	var in services.BookingInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	receipt, err := services.CreateBooking(in)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusCreated, receipt, "Booking created — complete the payment to confirm")
}

// ListRentals returns rentals with their car, customer and timeline.
// Supports ?status=, ?search=, ?sort= and ?page=/?per_page= pagination.
func ListRentals(c fiber.Ctx) error {
	page, paged, err := services.ParsePage(c, 20, 100)
	if err != nil {
		return respondError(c, err)
	}

	rentals, err := services.ListRentals(services.RentalFilter{
		Status: c.Query("status"),
		Search: c.Query("search"),
		Sort:   c.Query("sort"),
		Page:   page,
		Paged:  paged,
	})
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, rentals, "Rentals retrieved successfully")
}

// GetRental returns a single rental with its full timeline.
func GetRental(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	rental, err := services.GetRental(id)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, rental, "Rental retrieved successfully")
}

// BorrowRental hands a paid car over to its driver.
func BorrowRental(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	var in services.BorrowInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	rental, err := services.BorrowRental(id, in)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, rental, "Car handed over successfully")
}

// ReturnRental closes an active rental and settles any late fee.
func ReturnRental(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	var in services.ReturnInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	rental, err := services.ReturnRental(id, in)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, rental, "Car returned successfully")
}

// CancelRental cancels an unpaid booking.
func CancelRental(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	rental, err := services.CancelRental(id)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, rental, "Booking cancelled")
}

// Tracking lists the cars currently out on the road.
func Tracking(c fiber.Ctx) error {
	entries, err := services.Tracking()
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, entries, "Tracking data retrieved successfully")
}

// ExportRentalsCSV streams the filtered rental list as a CSV download.
func ExportRentalsCSV(c fiber.Ctx) error {
	body, filename, err := services.ExportRentalsCSV(services.RentalFilter{
		Status: c.Query("status"),
		Search: c.Query("search"),
		Sort:   c.Query("sort"),
	})
	if err != nil {
		return respondError(c, err)
	}
	return sendCSV(c, filename, body)
}
