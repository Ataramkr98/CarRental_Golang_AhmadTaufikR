package services

import (
	"math"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"
)

// validEmail performs a strict single-address parse, rejecting display names
// such as `Bob <bob@example.com>` that the booking form never sends.
func validEmail(address string) bool {
	parsed, err := mail.ParseAddress(address)
	return err == nil && strings.EqualFold(parsed.Address, address)
}

// normalizeStatus lower-cases and trims a status string coming from a client.
func normalizeStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

// isFinitePositive reports whether a float is a usable positive amount.
func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// limitField enforces a maximum length on free-text input. It is applied to
// every user-supplied string so a 1 MiB request body cannot be persisted into
// a column that is displayed in a table cell.
func limitField(field, value string, max int) error {
	if len([]rune(value)) > max {
		return Invalid(field + " must be at most " + strconv.Itoa(max) + " characters")
	}
	return nil
}

// textLimits is the shared length budget for the free-text fields, chosen to be
// generous for real data and small enough to stay readable in the console.
const (
	maxShortText  = 120
	maxLongText   = 500
	maxIdentifier = 32
)

// validateTextLengths applies limitField across a set of named fields.
func validateTextLengths(fields map[string]string) error {
	limits := map[string]int{
		"brand":        maxShortText,
		"model":        maxShortText,
		"plate":        maxIdentifier,
		"image url":    maxLongText,
		"name":         maxShortText,
		"email":        maxShortText,
		"phone":        maxIdentifier,
		"licence":      maxIdentifier,
		"description":  maxLongText,
		"driver name":  maxShortText,
		"driver phone": maxIdentifier,
		"pickup note":  maxLongText,
		"return note":  maxLongText,
		"booking note": maxLongText,
		"password":     128,
		"current pw":   128,
	}
	// Iterate deterministically so the first reported error never depends on
	// Go's map ordering.
	for _, field := range sortedFieldNames(fields) {
		if err := limitField(field, fields[field], limits[field]); err != nil {
			return err
		}
	}
	return nil
}

func sortedFieldNames(fields map[string]string) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// daysBetween counts whole rental days, rounding partial days up so a booking
// that spans a fraction of a day is still billed as one day.
func daysBetween(start, end time.Time) int {
	return int(math.Ceil(end.Sub(start).Hours() / 24))
}
