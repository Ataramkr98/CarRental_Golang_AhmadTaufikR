package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"gorm.io/gorm"

	"car-rental-backend/config"
	"car-rental-backend/models"
)

// CashCategories enumerates the ledger buckets a manual entry may use.
var CashCategories = map[string]bool{
	"rental_income": true,
	"late_fee":      true,
	"deposit":       true,
	"maintenance":   true,
	"fuel":          true,
	"salary":        true,
	"marketing":     true,
	"other":         true,
}

// CashFlowFilter narrows a ledger listing.
type CashFlowFilter struct {
	Type     string
	Category string
	Search   string
	From     *time.Time
	To       *time.Time
	Page     Page
	Paged    bool
}

// CashFlowInput is the writable shape of a manual ledger entry.
type CashFlowInput struct {
	Type        string     `json:"type"`
	Category    string     `json:"category"`
	Amount      float64    `json:"amount"`
	Description string     `json:"description"`
	Date        *time.Time `json:"date"`
}

// CashFlowSummaryResult aggregates the ledger.
type CashFlowSummaryResult struct {
	CashIn     float64       `json:"cash_in"`
	CashOut    float64       `json:"cash_out"`
	NetBalance float64       `json:"net_balance"`
	ByCategory []CategorySum `json:"by_category"`
}

// CategorySum is one bucket in the category breakdown.
type CategorySum struct {
	Category string  `json:"category"`
	Type     string  `json:"type"`
	Total    float64 `json:"total"`
}

// recordCash persists a ledger entry inside an existing transaction.
// Payment and rental flows call it so income is always logged atomically
// with the state change that produced it.
func recordCash(tx *gorm.DB, entry models.CashFlow) error {
	if entry.Date.IsZero() {
		entry.Date = time.Now()
	}
	return tx.Create(&entry).Error
}

// ListCashFlow returns ledger entries for the admin console.
func ListCashFlow(filter CashFlowFilter) (any, error) {
	query := config.DB.Model(&models.CashFlow{})

	if entryType := normalizeStatus(filter.Type); entryType != "" {
		if entryType != "in" && entryType != "out" {
			return nil, Invalid("type must be 'in' or 'out'")
		}
		query = query.Where("type = ?", entryType)
	}
	if category := normalizeStatus(filter.Category); category != "" {
		if !CashCategories[category] {
			return nil, Invalid("invalid cash flow category")
		}
		query = query.Where("category = ?", category)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		query = query.Where("LOWER(description) LIKE ?", "%"+strings.ToLower(search)+"%")
	}
	if filter.From != nil {
		query = query.Where("date >= ?", *filter.From)
	}
	if filter.To != nil {
		query = query.Where("date <= ?", *filter.To)
	}

	ordered := query.Order("date desc, id desc")

	if !filter.Paged {
		var entries []models.CashFlow
		if err := ordered.Find(&entries).Error; err != nil {
			return nil, err
		}
		return entries, nil
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var entries []models.CashFlow
	if err := filter.Page.Apply(ordered).Find(&entries).Error; err != nil {
		return nil, err
	}
	return NewPageResult(entries, total, filter.Page), nil
}

// CashFlowSummary returns totals and a per-category breakdown.
func CashFlowSummary() (*CashFlowSummaryResult, error) {
	var result CashFlowSummaryResult

	if err := config.DB.Model(&models.CashFlow{}).
		Where("type = ?", "in").
		Select("COALESCE(SUM(amount),0)").Scan(&result.CashIn).Error; err != nil {
		return nil, err
	}
	if err := config.DB.Model(&models.CashFlow{}).
		Where("type = ?", "out").
		Select("COALESCE(SUM(amount),0)").Scan(&result.CashOut).Error; err != nil {
		return nil, err
	}
	result.NetBalance = result.CashIn - result.CashOut

	if err := config.DB.Model(&models.CashFlow{}).
		Select("category, type, COALESCE(SUM(amount),0) as total").
		Group("category, type").
		Order("total desc").
		Scan(&result.ByCategory).Error; err != nil {
		return nil, err
	}
	if result.ByCategory == nil {
		result.ByCategory = []CategorySum{}
	}
	return &result, nil
}

// CreateCashFlow adds a manual ledger entry.
func CreateCashFlow(in CashFlowInput) (*models.CashFlow, error) {
	entry := models.CashFlow{
		Type:          normalizeStatus(in.Type),
		Category:      normalizeStatus(in.Category),
		Amount:        in.Amount,
		Description:   strings.TrimSpace(in.Description),
		ReferenceType: "manual",
	}
	if in.Date != nil {
		entry.Date = *in.Date
	}

	if entry.Type != "in" && entry.Type != "out" {
		return nil, Invalid("type must be 'in' or 'out'")
	}
	if !isFinitePositive(entry.Amount) {
		return nil, Invalid("amount must be greater than zero")
	}
	if entry.Description == "" {
		return nil, Invalid("a description is required")
	}
	if err := limitField("description", entry.Description, maxLongText); err != nil {
		return nil, err
	}
	if entry.Category == "" {
		entry.Category = "other"
	}
	if !CashCategories[entry.Category] {
		return nil, Invalid("invalid cash flow category")
	}
	if entry.Date.IsZero() {
		entry.Date = time.Now()
	}
	if entry.Date.After(time.Now().Add(24 * time.Hour)) {
		return nil, Invalid("the entry date cannot be in the future")
	}

	if err := config.DB.Create(&entry).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// DeleteCashFlow removes a manual entry. Automatic entries produced by
// payments and late fees are immutable parts of the rental audit trail.
func DeleteCashFlow(id uint) error {
	var entry models.CashFlow
	if err := config.DB.First(&entry, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NotFound("cash entry not found")
		}
		return err
	}
	if entry.ReferenceType != "manual" {
		return Conflict("automatic entries created by payments cannot be deleted")
	}
	return config.DB.Delete(&entry).Error
}

// ExportCashFlowCSV renders the filtered ledger as CSV.
func ExportCashFlowCSV(filter CashFlowFilter) (string, string, error) {
	filter.Paged = false
	raw, err := ListCashFlow(filter)
	if err != nil {
		return "", "", err
	}
	entries, ok := raw.([]models.CashFlow)
	if !ok {
		return "", "", fmt.Errorf("unexpected ledger type")
	}

	var builder strings.Builder
	builder.WriteString("Date,Type,Category,Description,Reference,Amount\n")
	for _, entry := range entries {
		signed := entry.Amount
		if entry.Type == "out" {
			signed = -entry.Amount
		}
		fmt.Fprintf(&builder, "%s,%s,%s,%s,%s,%.2f\n",
			entry.Date.Format("2006-01-02"),
			csvCell(entry.Type),
			csvCell(entry.Category),
			csvCell(entry.Description),
			csvCell(entry.ReferenceType),
			signed,
		)
	}
	return builder.String(), "cashflow.csv", nil
}

// csvCell escapes a value for CSV output.
func csvCell(value string) string {
	if !strings.ContainsAny(value, ",\"\n") {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// QueryTime parses an optional RFC3339 or YYYY-MM-DD query parameter.
func QueryTime(c fiber.Ctx, key string) (*time.Time, error) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return &parsed, nil
		}
	}
	return nil, Invalid(key + " must be a date (YYYY-MM-DD) or RFC3339 timestamp")
}
