package services

import (
	"strconv"
	"strings"

	"car-rental-backend/config"
	"car-rental-backend/models"
)

// ActivityEntry is a rental event enriched with the car and customer it
// belongs to, ready for the audit-log table.
type ActivityEntry struct {
	models.RentalEvent
	CarBrand     string `json:"car_brand"`
	CarModel     string `json:"car_model"`
	CarPlate     string `json:"car_plate"`
	CustomerName string `json:"customer_name"`
}

// ActivityFilter narrows the audit log.
type ActivityFilter struct {
	RentalID uint
	Status   string
	Search   string
	Limit    int
	Page     Page
	Paged    bool
}

// ListActivity returns the audit trail, newest first.
func ListActivity(filter ActivityFilter) (any, error) {
	if filter.Status != "" && !RentalStatuses[normalizeStatus(filter.Status)] {
		return nil, Invalid("invalid rental status")
	}

	base := config.DB.Model(&models.RentalEvent{}).
		Joins("JOIN rentals ON rentals.id = rental_events.rental_id").
		Joins("JOIN cars ON cars.id = rentals.car_id").
		Joins("LEFT JOIN customers ON customers.id = rentals.customer_id")

	if filter.RentalID > 0 {
		base = base.Where("rental_events.rental_id = ?", filter.RentalID)
	}
	if filter.Status != "" {
		base = base.Where("rental_events.to_status = ?", normalizeStatus(filter.Status))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		base = base.Where(
			"LOWER(rental_events.note) LIKE ? OR LOWER(cars.brand) LIKE ? OR LOWER(cars.model) LIKE ? OR LOWER(cars.plate) LIKE ? OR LOWER(customers.name) LIKE ?",
			like, like, like, like, like)
	}

	selection := `rental_events.*,
		cars.brand AS car_brand, cars.model AS car_model, cars.plate AS car_plate,
		customers.name AS customer_name`

	if !filter.Paged {
		limit := filter.Limit
		if limit < 1 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		var entries []ActivityEntry
		if err := base.Select(selection).
			Order("rental_events.created_at DESC, rental_events.id DESC").
			Limit(limit).
			Scan(&entries).Error; err != nil {
			return nil, err
		}
		if entries == nil {
			entries = []ActivityEntry{}
		}
		return entries, nil
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, err
	}
	var entries []ActivityEntry
	if err := base.Select(selection).
		Order("rental_events.created_at DESC, rental_events.id DESC").
		Limit(filter.Page.PerPage).Offset(filter.Page.Offset()).
		Scan(&entries).Error; err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []ActivityEntry{}
	}
	return NewPageResult(entries, total, filter.Page), nil
}

// ParseLimitQuery reads an optional ?limit= page-size override.
//
// It lets a caller fetch "the most recent N events" without paginating, which
// is what the dashboard and the console's activity widget want. Values outside
// [1, max] are clamped rather than rejected, so a large limit degrades
// gracefully instead of failing the request.
func ParseLimitQuery(raw string, fallback, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, Invalid("limit must be a positive integer")
	}
	if value > max {
		return max, nil
	}
	return value, nil
}

// ParseUintQuery reads an optional positive integer query parameter.
func ParseUintQuery(raw string) (uint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, Invalid("expected a numeric identifier")
	}
	return uint(value), nil
}
