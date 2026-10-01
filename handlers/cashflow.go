package handlers

import (
	"github.com/gofiber/fiber/v3"

	"car-rental-backend/services"
)

// ListCashFlow returns ledger entries with optional filters and pagination.
func ListCashFlow(c fiber.Ctx) error {
	page, paged, err := services.ParsePage(c, 20, 200)
	if err != nil {
		return respondError(c, err)
	}
	from, err := services.QueryTime(c, "from")
	if err != nil {
		return respondError(c, err)
	}
	to, err := services.QueryTime(c, "to")
	if err != nil {
		return respondError(c, err)
	}

	entries, err := services.ListCashFlow(services.CashFlowFilter{
		Type:     c.Query("type"),
		Category: c.Query("category"),
		Search:   c.Query("search"),
		From:     from,
		To:       to,
		Page:     page,
		Paged:    paged,
	})
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, entries, "Entries retrieved successfully")
}

// CashFlowSummary returns totals plus a per-category breakdown.
func CashFlowSummary(c fiber.Ctx) error {
	summary, err := services.CashFlowSummary()
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, summary, "Cash flow summary retrieved")
}

// CreateCashFlow adds a manual ledger entry.
func CreateCashFlow(c fiber.Ctx) error {
	var in services.CashFlowInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	entry, err := services.CreateCashFlow(in)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusCreated, entry, "Entry recorded")
}

// DeleteCashFlow removes a manual ledger entry.
func DeleteCashFlow(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	if err := services.DeleteCashFlow(id); err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, fiber.Map{"deleted": true}, "Entry deleted")
}

// ExportCashFlowCSV streams the filtered ledger as a CSV download.
func ExportCashFlowCSV(c fiber.Ctx) error {
	from, err := services.QueryTime(c, "from")
	if err != nil {
		return respondError(c, err)
	}
	to, err := services.QueryTime(c, "to")
	if err != nil {
		return respondError(c, err)
	}

	body, filename, err := services.ExportCashFlowCSV(services.CashFlowFilter{
		Type:     c.Query("type"),
		Category: c.Query("category"),
		Search:   c.Query("search"),
		From:     from,
		To:       to,
	})
	if err != nil {
		return respondError(c, err)
	}
	return sendCSV(c, filename, body)
}
