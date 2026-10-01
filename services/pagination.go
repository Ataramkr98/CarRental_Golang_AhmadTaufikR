package services

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"gorm.io/gorm"
)

// Page describes one requested slice of a collection.
type Page struct {
	Number  int
	PerPage int
}

// PageResult is the paginated collection envelope.
type PageResult struct {
	Items   any   `json:"items"`
	Total   int64 `json:"total"`
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
	Pages   int   `json:"pages"`
}

// ParseID converts a route parameter into a primary key.
//
// Every id-bearing route funnels through here so a malformed segment is a
// client error (400) instead of a database lookup that fails as a 500.
func ParseID(raw string) (uint, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil || parsed == 0 {
		return 0, Invalid("the requested record id is not valid")
	}
	return uint(parsed), nil
}

// ParsePage reads ?page and ?per_page from the request.
//
// The second return value reports whether the client asked for pagination at
// all. When it is false the caller should keep returning a plain array, which
// keeps the API backward compatible for consumers that do not paginate.
func ParsePage(c fiber.Ctx, defaultPerPage, maxPerPage int) (Page, bool, error) {
	perPage := defaultPerPage
	if rawPerPage := strings.TrimSpace(c.Query("per_page")); rawPerPage != "" {
		parsed, err := strconv.Atoi(rawPerPage)
		if err != nil || parsed < 1 {
			return Page{}, true, Invalid("per_page must be a positive integer")
		}
		perPage = parsed
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}

	raw := strings.TrimSpace(c.Query("page"))
	if raw == "" {
		// No page requested: the caller returns the whole collection, so the
		// page size is irrelevant beyond the cap applied above.
		return Page{}, false, nil
	}

	number, err := strconv.Atoi(raw)
	if err != nil || number < 1 {
		return Page{}, true, Invalid("page must be a positive integer")
	}
	if number > maxPage {
		// A page far past the end is a client mistake, not a full-table scan.
		return Page{}, true, Invalid("page is out of range")
	}

	return Page{Number: number, PerPage: perPage}, true, nil
}

// maxPage bounds ?page so a hostile value cannot force an unbounded OFFSET.
const maxPage = 100_000

// Offset returns the SQL offset for the page.
func (p Page) Offset() int { return (p.Number - 1) * p.PerPage }

// Apply adds LIMIT/OFFSET to a GORM query.
func (p Page) Apply(query *gorm.DB) *gorm.DB {
	return query.Limit(p.PerPage).Offset(p.Offset())
}

// NewPageResult builds the envelope for a page of results. A collection with
// no rows still reports one page, so a client never has to special-case zero.
func NewPageResult(items any, total int64, page Page) PageResult {
	pages := 1
	if total > 0 {
		pages = int((total + int64(page.PerPage) - 1) / int64(page.PerPage))
	}
	return PageResult{
		Items:   items,
		Total:   total,
		Page:    page.Number,
		PerPage: page.PerPage,
		Pages:   pages,
	}
}
