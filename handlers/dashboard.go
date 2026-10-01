package handlers

import (
	"github.com/gofiber/fiber/v3"

	"car-rental-backend/services"
)

// DashboardStats returns every aggregate the admin dashboard renders.
func DashboardStats(c fiber.Ctx) error {
	stats, err := services.GetDashboardStats()
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, stats, "Dashboard statistics retrieved")
}

// RevenueSeries returns the cash-flow timeseries used by the dashboard chart.
// Range is one of ?range=7d|14d|30d|6m (defaults to 7d).
func RevenueSeries(c fiber.Ctx) error {
	series, err := services.RevenueSeriesForRange(c.Query("range"))
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, series, "Cash flow series retrieved")
}

// ListActivity returns the rental audit trail.
func ListActivity(c fiber.Ctx) error {
	page, paged, err := services.ParsePage(c, 25, 200)
	if err != nil {
		return respondError(c, err)
	}
	rentalID, err := services.ParseUintQuery(c.Query("rental_id"))
	if err != nil {
		return respondError(c, err)
	}

	// Without ?page= the endpoint returns a plain array capped at ?limit=,
	// which is what the console uses for its "latest events" view.
	limit, err := services.ParseLimitQuery(c.Query("limit"), 50, 200)
	if err != nil {
		return respondError(c, err)
	}

	entries, err := services.ListActivity(services.ActivityFilter{
		RentalID: rentalID,
		Status:   c.Query("status"),
		Search:   c.Query("search"),
		Limit:    limit,
		Page:     page,
		Paged:    paged,
	})
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, entries, "Activity retrieved successfully")
}
