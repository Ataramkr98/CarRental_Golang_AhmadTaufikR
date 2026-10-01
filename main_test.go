package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"car-rental-backend/config"
	"car-rental-backend/models"

	"github.com/gofiber/fiber/v3"
)

// newTestApp boots the full app against the configured PostgreSQL database in
// mock mode. Tests share the same database but seed data is idempotent (only
// seeds when tables are empty).
func newTestApp(t *testing.T) *fiber.App {
	t.Helper()
	config.Close()
	// DATABASE_URL must be set in .env or environment before running tests.
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	os.Setenv("JWT_SECRET", "test-only-secret-not-used-in-production")
	os.Setenv("PORT", "0")
	os.Setenv("XENDIT_MOCK", "true")
	os.Setenv("XENDIT_SECRET_KEY", "")
	// The webhook fails closed without a callback token, so the suite always
	// configures one — exactly as a live deployment must.
	os.Setenv("XENDIT_CALLBACK_TOKEN", testCallbackToken)
	os.Setenv("LATE_FEE_MULTIPLIER", "0.5")
	// Registration is opt-in; integration tests exercise both states explicitly.
	os.Unsetenv("ALLOW_REGISTRATION")
	os.Unsetenv("SMTP_HOST")
	app := setupApp()
	// Tests share one long-lived database. Prior runs leave paid/pending
	// bookings that still reserve inventory and make later invoice creation
	// 409. Cancel those open (non-active) rentals and free their cars so each
	// test starts with a clean fleet; cars actually on the road stay rented.
	resetFleetForTests(t)
	return app
}

// resetFleetForTests cancels open bookings left by previous suite runs and
// returns every car that is not on an active rental to `available`.
func resetFleetForTests(t *testing.T) {
	t.Helper()
	if err := config.DB.Exec(`
		UPDATE rentals SET status = 'cancelled'
		WHERE status IN ('pending', 'paid')`).Error; err != nil {
		t.Fatalf("cancel open rentals: %v", err)
	}
	if err := config.DB.Exec(`
		UPDATE cars SET status = 'available'
		WHERE status IN ('reserved', 'rented')
		  AND id NOT IN (
		    SELECT car_id FROM rentals
		    WHERE status = 'active' AND car_id > 0
		  )`).Error; err != nil {
		t.Fatalf("release stale reserved cars: %v", err)
	}
}

// decodeObject unwraps the API's standard {success, data, message} envelope so
// tests assert against the payload directly. Bodies that are not enveloped
// (plain maps, error shapes) are returned unchanged.
func decodeObject(raw []byte) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	if _, enveloped := out["success"]; enveloped {
		if data, ok := out["data"].(map[string]any); ok {
			return data
		}
	}
	return out
}

// decodeList unwraps a list response in either of the two shapes the API uses:
// a bare array (`data: [...]`) or a paginated envelope
// (`data: { items: [...], total, page, per_page, pages }`).
func decodeList(raw []byte) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	toMaps := func(items []any) []map[string]any {
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}

	var env map[string]any
	if err := json.Unmarshal(raw, &env); err == nil {
		switch data := env["data"].(type) {
		case []any:
			return toMaps(data)
		case map[string]any:
			if items, ok := data["items"].([]any); ok {
				return toMaps(items)
			}
		}
	}

	var out []map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// testCallbackToken is the XENDIT_CALLBACK_TOKEN every test runs with.
const testCallbackToken = "test-callback-token"

// do sends a request through the app in-process and returns the unwrapped payload.
func do(t *testing.T, app *fiber.App, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	return doHeaders(t, app, method, path, token, nil, body)
}

// doHeaders is do plus extra request headers, used for the guest checkout token
// and the payment-gateway callback token.
func doHeaders(t *testing.T, app *fiber.App, method, path, token string, headers map[string]string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("app.Test(%s %s): %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, decodeObject(raw)
}

// doCheckout sends a guest checkout call with the booking's one-time token.
func doCheckout(t *testing.T, app *fiber.App, method, path, checkoutToken string, body any) (int, map[string]any) {
	t.Helper()
	headers := map[string]string{}
	if checkoutToken != "" {
		headers["x-checkout-token"] = checkoutToken
	}
	return doHeaders(t, app, method, path, "", headers, body)
}

// doList sends a request and returns the unwrapped list payload.
func doList(t *testing.T, app *fiber.App, method, path, token string) (int, []map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("app.Test(%s %s): %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, decodeList(raw)
}

func itoa(u uint) string { return strconv.FormatUint(uint64(u), 10) }

func login(t *testing.T, app *fiber.App) string {
	t.Helper()
	status, body := do(t, app, "POST", "/api/auth/login", "", map[string]any{
		"email": "admin@rental.dev", "password": "admin123",
	})
	if status != 200 {
		t.Fatalf("login: want 200, got %d (%v)", status, body)
	}
	tok, _ := body["token"].(string)
	if tok == "" {
		t.Fatalf("login: empty token")
	}
	return tok
}

// loginAs signs in as the seeded operator account (demo@rental.dev).
func loginAs(t *testing.T, app *fiber.App, email, password string) string {
	t.Helper()
	status, body := do(t, app, "POST", "/api/auth/login", "", map[string]any{
		"email": email, "password": password,
	})
	if status != 200 {
		t.Fatalf("login %s: want 200, got %d (%v)", email, status, body)
	}
	tok, _ := body["token"].(string)
	if tok == "" {
		t.Fatalf("login %s: empty token", email)
	}
	return tok
}

// bookCar creates a public booking for an available car and returns the rental
// together with its one-time checkout token, which every later guest call
// (pay, mock-pay, status) has to present.
func bookCar(t *testing.T, app *fiber.App, start, end time.Time) (map[string]any, string) {
	t.Helper()
	_, cars := doList(t, app, "GET", "/api/cars", "")
	if len(cars) == 0 {
		t.Fatalf("no available cars to book")
	}
	carID := uint(cars[0]["id"].(float64))
	status, booking := do(t, app, "POST", "/api/bookings", "", map[string]any{
		"car_id":     carID,
		"start_date": start.Format(time.RFC3339),
		"end_date":   end.Format(time.RFC3339),
		"customer": map[string]any{
			"name": "Test User", "email": "test@example.com",
			"phone": "081200001111", "license": "SIM-TEST",
		},
	})
	if status != 201 {
		t.Fatalf("booking: want 201, got %d (%v)", status, booking)
	}
	token, _ := booking["checkout_token"].(string)
	if token == "" {
		t.Fatalf("booking: response carried no checkout_token: %v", booking)
	}
	return booking, token
}

func TestHealth(t *testing.T) {
	app := newTestApp(t)
	status, body := do(t, app, "GET", "/api/health", "", nil)
	if status != 200 || body["status"] != "ok" {
		t.Fatalf("health: want 200/ok, got %d/%v", status, body["status"])
	}
	t.Logf("PASS GET /api/health -> 200 ok")
}

func TestPublicCarsOnlyAvailable(t *testing.T) {
	app := newTestApp(t)
	status, cars := doList(t, app, "GET", "/api/cars", "")
	if status != 200 || len(cars) == 0 {
		t.Fatalf("public cars: want 200 with cars, got %d/%d", status, len(cars))
	}
	for _, c := range cars {
		if c["status"] != "available" {
			t.Fatalf("public cars: expected only available, got %v", c["status"])
		}
	}
	t.Logf("PASS GET /api/cars -> 200, %d available cars", len(cars))
}

func TestAdminRequiresAuth(t *testing.T) {
	app := newTestApp(t)
	status, _ := do(t, app, "GET", "/api/admin/dashboard/stats", "", nil)
	if status != 401 {
		t.Fatalf("admin without token: want 401, got %d", status)
	}
	t.Logf("PASS GET /api/admin/dashboard/stats (no token) -> 401")
}

func TestLoginBadCredentials(t *testing.T) {
	app := newTestApp(t)
	status, _ := do(t, app, "POST", "/api/auth/login", "", map[string]any{
		"email": "admin@rental.dev", "password": "wrong-password",
	})
	if status != 401 {
		t.Fatalf("bad login: want 401, got %d", status)
	}
	t.Logf("PASS POST /api/auth/login (wrong password) -> 401")
}

func TestDashboardStats(t *testing.T) {
	app := newTestApp(t)
	tok := login(t, app)
	status, body := do(t, app, "GET", "/api/admin/dashboard/stats", tok, nil)
	if status != 200 {
		t.Fatalf("stats: want 200, got %d (%v)", status, body)
	}
	if _, ok := body["cars"].(map[string]any); !ok {
		t.Fatalf("stats: missing cars block: %v", body)
	}
	if _, ok := body["cash_in"]; !ok {
		t.Fatalf("stats: missing cash_in: %v", body)
	}
	t.Logf("PASS stats -> cars=%v cash_in=%v net=%v", body["cars"], body["cash_in"], body["net_balance"])
}

// TestPaymentFlow: book -> create (mock) invoice -> simulate payment -> paid,
// and verify the rental income is recorded in the cash ledger.
func TestPaymentFlow(t *testing.T) {
	app := newTestApp(t)
	tok := login(t, app)

	booking, checkout := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 4))
	rid := uint(booking["id"].(float64))
	if booking["status"] != "pending" || booking["payment_status"] != "unpaid" {
		t.Fatalf("booking: want pending/unpaid, got %v/%v", booking["status"], booking["payment_status"])
	}
	// The public receipt must never carry customer or driver data.
	for _, forbidden := range []string{"customer", "driver_name", "driver_phone", "driver_license"} {
		if _, leaked := booking[forbidden]; leaked {
			t.Fatalf("booking receipt leaked %s", forbidden)
		}
	}

	// create invoice (mock)
	status, inv := doCheckout(t, app, "POST", "/api/rentals/"+itoa(rid)+"/pay", checkout, nil)
	if status != 200 || inv["mock"] != true {
		t.Fatalf("create invoice: want 200/mock, got %d/%v", status, inv["mock"])
	}

	// simulate successful payment
	status, paid := doCheckout(t, app, "POST", "/api/rentals/"+itoa(rid)+"/mock-pay", checkout, nil)
	if status != 200 {
		t.Fatalf("mock-pay: want 200, got %d (%v)", status, paid)
	}
	rental := paid["rental"].(map[string]any)
	if rental["status"] != "paid" || rental["payment_status"] != "paid" {
		t.Fatalf("after pay: want paid/paid, got %v/%v", rental["status"], rental["payment_status"])
	}

	// rental income should be in the cash ledger
	_, ledger := doList(t, app, "GET", "/api/admin/cashflow", tok)
	found := false
	for _, e := range ledger {
		if e["category"] == "rental_income" && e["reference_type"] == "rental" && uint(e["reference_id"].(float64)) == rid {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a rental_income cash entry for rental #%d", rid)
	}
	t.Logf("PASS payment flow: booking -> mock invoice -> paid, income logged to cash ledger")
}

// TestCheckoutRequiresBookingToken is the authorisation guard on the guest
// checkout: sequential rental ids are not a credential. Without the booking's
// own token (or a console session) the pay / mock-pay / status routes must
// refuse, and must refuse before changing any state.
func TestCheckoutRequiresBookingToken(t *testing.T) {
	app := newTestApp(t)
	booking, checkout := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 4))
	rid := uint(booking["id"].(float64))
	path := "/api/rentals/" + itoa(rid)

	// No credentials at all.
	if status, _ := do(t, app, "POST", path+"/pay", "", nil); status != 401 {
		t.Fatalf("anonymous pay: want 401, got %d", status)
	}
	if status, _ := do(t, app, "POST", path+"/mock-pay", "", nil); status != 401 {
		t.Fatalf("anonymous mock-pay: want 401, got %d", status)
	}
	if status, _ := do(t, app, "GET", path+"/status", "", nil); status != 401 {
		t.Fatalf("anonymous status: want 401, got %d", status)
	}

	// A wrong token is rejected the same way.
	if status, _ := doCheckout(t, app, "POST", path+"/pay", "not-the-token", nil); status != 401 {
		t.Fatalf("wrong checkout token: want 401, got %d", status)
	}

	// The correct token works, which proves the rejections above were the token
	// check and not a broken route.
	if status, _ := doCheckout(t, app, "GET", path+"/status", checkout, nil); status != 200 {
		t.Fatalf("valid checkout token: want 200, got %d", status)
	}

	// A console session is also accepted: that is how the operator's "Mark paid"
	// action reaches the same endpoint.
	adminToken := login(t, app)
	if status, _ := do(t, app, "POST", path+"/mock-pay", adminToken, nil); status != 200 {
		t.Fatalf("operator mock-pay: want 200, got %d", status)
	}
	t.Logf("PASS checkout auth: anonymous 401, wrong token 401, booking token 200, console 200")
}

// TestXenditWebhookRequiresCallbackToken: the gateway callback fails closed.
func TestXenditWebhookRequiresCallbackToken(t *testing.T) {
	app := newTestApp(t)
	booking, _ := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 3))
	rid := uint(booking["id"].(float64))
	payload := map[string]any{"external_id": "rental-" + itoa(rid), "status": "PAID"}

	// A callback with no token must not be able to mark a booking paid.
	status, _ := do(t, app, "POST", "/api/webhooks/xendit", "", payload)
	if status != 401 {
		t.Fatalf("unauthenticated webhook: want 401, got %d", status)
	}
	_, stillPending := doList(t, app, "GET", "/api/admin/rentals", login(t, app))
	for _, r := range stillPending {
		if uint(r["id"].(float64)) == rid && r["payment_status"] == "paid" {
			t.Fatalf("unauthenticated webhook still marked rental #%d paid", rid)
		}
	}

	// A PAID callback without an amount is rejected even with a valid token.
	status, _ = doHeaders(t, app, "POST", "/api/webhooks/xendit", "", map[string]string{
		"x-callback-token": testCallbackToken,
	}, map[string]any{"external_id": "rental-" + itoa(rid), "status": "PAID"})
	if status != 400 {
		t.Fatalf("webhook without paid_amount: want 400, got %d", status)
	}
	t.Logf("PASS webhook auth: missing token 401, missing amount 400")
}

// TestXenditWebhook: a verified PAID callback marks the rental paid.
func TestXenditWebhook(t *testing.T) {
	app := newTestApp(t)
	tok := login(t, app)

	booking, _ := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 3))
	rid := uint(booking["id"].(float64))

	status, _ := doHeaders(t, app, "POST", "/api/webhooks/xendit", "", map[string]string{
		"x-callback-token": testCallbackToken,
	}, map[string]any{
		"external_id": "rental-" + itoa(rid), "status": "PAID", "paid_amount": booking["total_price"],
	})
	if status != 200 {
		t.Fatalf("webhook: want 200, got %d", status)
	}

	_, rentals := doList(t, app, "GET", "/api/admin/rentals", tok)
	ok := false
	for _, r := range rentals {
		if uint(r["id"].(float64)) == rid && r["payment_status"] == "paid" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("webhook did not mark rental #%d paid", rid)
	}
	t.Logf("PASS Xendit webhook PAID -> rental #%d marked paid", rid)
}

// TestBorrowTrackReturnLateFee: full library-style lifecycle on an overdue
// rental — pay, borrow (appears in tracking), return (late fee charged).
func TestBorrowTrackReturnLateFee(t *testing.T) {
	app := newTestApp(t)
	tok := login(t, app)

	booking, checkout := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 4))
	rid := uint(booking["id"].(float64))

	// pay it
	if status, _ := doCheckout(t, app, "POST", "/api/rentals/"+itoa(rid)+"/mock-pay", checkout, nil); status != 200 {
		t.Fatalf("mock-pay: want 200, got %d", status)
	}
	// Move the paid test rental into the past to exercise the overdue return
	// path without weakening public booking date validation.
	if err := config.DB.Model(&models.Rental{}).Where("id = ?", rid).Updates(map[string]any{
		"start_date": time.Now().AddDate(0, 0, -5),
		"end_date":   time.Now().AddDate(0, 0, -2),
	}).Error; err != nil {
		t.Fatalf("make rental overdue: %v", err)
	}

	// borrow (hand over)
	status, borrowed := do(t, app, "PUT", "/api/admin/rentals/"+itoa(rid)+"/borrow", tok, map[string]any{
		"driver_name": "Tester", "driver_license": "SIM-X", "odometer_out": 1000, "pickup_note": "full tank",
	})
	if status != 200 || borrowed["status"] != "active" {
		t.Fatalf("borrow: want 200/active, got %d/%v", status, borrowed["status"])
	}

	// it should now appear in live tracking
	_, tracking := doList(t, app, "GET", "/api/admin/tracking", tok)
	found := false
	for _, tr := range tracking {
		if uint(tr["id"].(float64)) == rid {
			found = true
			if tr["overdue"] != true {
				t.Fatalf("tracking: expected overdue=true for rental #%d", rid)
			}
		}
	}
	if !found {
		t.Fatalf("active rental #%d not found in tracking", rid)
	}

	// return (overdue -> late fee charged)
	status, returned := do(t, app, "PUT", "/api/admin/rentals/"+itoa(rid)+"/return", tok, map[string]any{
		"odometer_in": 1300, "return_note": "returned late",
	})
	if status != 200 || returned["status"] != "returned" {
		t.Fatalf("return: want 200/returned, got %d/%v", status, returned["status"])
	}
	lateFee, _ := returned["late_fee"].(float64)
	if lateFee <= 0 {
		t.Fatalf("expected late fee > 0 for overdue return, got %v", lateFee)
	}

	// late fee should be in the cash ledger
	_, ledger := doList(t, app, "GET", "/api/admin/cashflow", tok)
	lf := false
	for _, e := range ledger {
		if e["category"] == "late_fee" && uint(e["reference_id"].(float64)) == rid {
			lf = true
		}
	}
	if !lf {
		t.Fatalf("expected a late_fee cash entry for rental #%d", rid)
	}
	t.Logf("PASS lifecycle: pay -> borrow -> tracking(overdue) -> return with late fee $%.2f", lateFee)
}

// TestCashFlowCRUD: manual create / list / summary / delete.
func TestCashFlowCRUD(t *testing.T) {
	app := newTestApp(t)
	tok := login(t, app)

	status, summary := do(t, app, "GET", "/api/admin/cashflow/summary", tok, nil)
	if status != 200 {
		t.Fatalf("summary: want 200, got %d", status)
	}
	if summary["cash_in"].(float64) <= 0 {
		t.Fatalf("expected seeded cash_in > 0, got %v", summary["cash_in"])
	}

	status, created := do(t, app, "POST", "/api/admin/cashflow", tok, map[string]any{
		"type": "out", "category": "fuel", "amount": 50.0, "description": "Test fuel expense",
	})
	if status != 201 {
		t.Fatalf("create cashflow: want 201, got %d (%v)", status, created)
	}
	cid := uint(created["id"].(float64))

	status, list := doList(t, app, "GET", "/api/admin/cashflow", tok)
	if status != 200 || len(list) == 0 {
		t.Fatalf("list cashflow: want 200 with entries, got %d/%d", status, len(list))
	}

	status, _ = do(t, app, "DELETE", "/api/admin/cashflow/"+itoa(cid), tok, nil)
	if status != 200 {
		t.Fatalf("delete cashflow: want 200, got %d", status)
	}
	t.Logf("PASS cash flow CRUD: summary(cash_in>0), create, list, delete")
}

func TestBookingValidation(t *testing.T) {
	app := newTestApp(t)
	_, cars := doList(t, app, "GET", "/api/cars", "")
	if len(cars) == 0 {
		t.Fatalf("no available cars to validate against")
	}
	carID := uint(cars[0]["id"].(float64))

	status, _ := do(t, app, "POST", "/api/bookings", "", map[string]any{
		"car_id":     carID,
		"start_date": time.Now().AddDate(0, 0, 2).Format(time.RFC3339),
		"end_date":   time.Now().AddDate(0, 0, 1).Format(time.RFC3339),
		"customer":   map[string]any{"name": "Test", "email": "test@example.com", "phone": "123", "license": "SIM-X"},
	})
	if status != 400 {
		t.Fatalf("reversed dates: want 400, got %d", status)
	}

	status, _ = do(t, app, "POST", "/api/bookings", "", map[string]any{
		"car_id":     carID,
		"start_date": time.Now().AddDate(0, 0, 1).Format(time.RFC3339),
		"end_date":   time.Now().AddDate(0, 0, 2).Format(time.RFC3339),
		"customer":   map[string]any{"name": "Test", "email": "not-an-email", "phone": "123", "license": "SIM-X"},
	})
	if status != 400 {
		t.Fatalf("invalid email: want 400, got %d", status)
	}
}

func TestInvoiceReservationReuseAndConflict(t *testing.T) {
	app := newTestApp(t)
	start := time.Now().AddDate(0, 0, 1)
	end := time.Now().AddDate(0, 0, 3)
	first, firstCheckout := bookCar(t, app, start, end)
	carID := uint(first["car_id"].(float64))

	status, second := do(t, app, "POST", "/api/bookings", "", map[string]any{
		"car_id": carID, "start_date": start.Format(time.RFC3339), "end_date": end.Format(time.RFC3339),
		"customer": map[string]any{"name": "Second", "email": "second@example.com", "phone": "456", "license": "SIM-Y"},
	})
	if status != 201 {
		t.Fatalf("second pending booking: want 201, got %d", status)
	}
	secondCheckout, _ := second["checkout_token"].(string)

	firstID := uint(first["id"].(float64))
	status, invoice := doCheckout(t, app, "POST", "/api/rentals/"+itoa(firstID)+"/pay", firstCheckout, nil)
	if status != 200 {
		t.Fatalf("first invoice: want 200, got %d", status)
	}
	status, reused := doCheckout(t, app, "POST", "/api/rentals/"+itoa(firstID)+"/pay", firstCheckout, nil)
	if status != 200 || reused["reused"] != true || reused["invoice_id"] != invoice["invoice_id"] {
		t.Fatalf("invoice should be reused: status=%d body=%v", status, reused)
	}

	secondID := uint(second["id"].(float64))
	status, _ = doCheckout(t, app, "POST", "/api/rentals/"+itoa(secondID)+"/pay", secondCheckout, nil)
	if status != 409 {
		t.Fatalf("conflicting invoice: want 409, got %d", status)
	}

	_, available := doList(t, app, "GET", "/api/cars", "")
	for _, car := range available {
		if uint(car["id"].(float64)) == carID {
			t.Fatalf("reserved car still appears in public fleet")
		}
	}
}

func TestInvalidLifecycleTransitions(t *testing.T) {
	app := newTestApp(t)
	token := login(t, app)
	booking, checkout := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 3))
	rid := uint(booking["id"].(float64))

	status, _ := do(t, app, "PUT", "/api/admin/rentals/"+itoa(rid)+"/cancel", token, nil)
	if status != 200 {
		t.Fatalf("cancel pending: want 200, got %d", status)
	}
	status, _ = doCheckout(t, app, "POST", "/api/rentals/"+itoa(rid)+"/mock-pay", checkout, nil)
	if status != 409 {
		t.Fatalf("pay cancelled rental: want 409, got %d", status)
	}

	paid, paidCheckout := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 3))
	paidID := uint(paid["id"].(float64))
	status, _ = doCheckout(t, app, "POST", "/api/rentals/"+itoa(paidID)+"/mock-pay", paidCheckout, nil)
	if status != 200 {
		t.Fatalf("mock pay: want 200, got %d", status)
	}
	status, _ = do(t, app, "PUT", "/api/admin/rentals/"+itoa(paidID)+"/cancel", token, nil)
	if status != 409 {
		t.Fatalf("cancel paid rental: want 409, got %d", status)
	}
}

func TestReturnRejectsOdometerRollback(t *testing.T) {
	app := newTestApp(t)
	token := login(t, app)
	booking, checkout := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 3))
	rid := uint(booking["id"].(float64))
	doCheckout(t, app, "POST", "/api/rentals/"+itoa(rid)+"/mock-pay", checkout, nil)
	do(t, app, "PUT", "/api/admin/rentals/"+itoa(rid)+"/borrow", token, map[string]any{
		"driver_name": "Tester", "driver_license": "SIM-X", "driver_phone": "123", "odometer_out": 1000,
	})
	status, _ := do(t, app, "PUT", "/api/admin/rentals/"+itoa(rid)+"/return", token, map[string]any{"odometer_in": 999})
	if status != 400 {
		t.Fatalf("odometer rollback: want 400, got %d", status)
	}
}

func TestAutomaticCashEntryCannotBeDeleted(t *testing.T) {
	app := newTestApp(t)
	token := login(t, app)
	_, entries := doList(t, app, "GET", "/api/admin/cashflow", token)
	var automaticID uint
	for _, entry := range entries {
		if entry["reference_type"] == "rental" {
			automaticID = uint(entry["id"].(float64))
			break
		}
	}
	if automaticID == 0 {
		t.Fatal("expected seeded automatic cash entry")
	}
	status, _ := do(t, app, "DELETE", "/api/admin/cashflow/"+itoa(automaticID), token, nil)
	if status != 409 {
		t.Fatalf("delete automatic entry: want 409, got %d", status)
	}
}

func TestPublicRentalStatusIsSanitized(t *testing.T) {
	app := newTestApp(t)
	booking, checkout := bookCar(t, app, time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 3))
	rid := uint(booking["id"].(float64))
	status, body := doCheckout(t, app, "GET", "/api/rentals/"+itoa(rid)+"/status", checkout, nil)
	if status != 200 {
		t.Fatalf("rental status: want 200, got %d", status)
	}
	for _, forbidden := range []string{"customer", "driver_name", "driver_phone", "driver_license"} {
		if _, exists := body[forbidden]; exists {
			t.Fatalf("public status leaked %s", forbidden)
		}
	}
}

// TestMalformedIDsAreRejected: a bad path segment is a client error, not a
// 500 from the database layer. A well-formed id that simply does not exist is
// still a 404, which is asserted separately in the same loop.
func TestMalformedIDsAreRejected(t *testing.T) {
	app := newTestApp(t)
	tok := login(t, app)
	for _, path := range []string{
		"GET /api/admin/cars/abc",
		"GET /api/admin/cars/0",
		"GET /api/admin/customers/-1",
		"GET /api/admin/rentals/not-a-number",
		"GET /api/admin/rentals/1e5",
		"PUT /api/admin/cars/xyz",
		"DELETE /api/admin/cashflow/abc",
		"PUT /api/admin/customers/%20",
	} {
		parts := strings.SplitN(path, " ", 2)
		method, target := parts[0], parts[1]
		status, _ := do(t, app, method, target, tok, nil)
		if status != 400 {
			t.Fatalf("%s: want 400 for a malformed id, got %d", path, status)
		}
	}
	// A syntactically valid id that matches nothing is a 404, not a 400.
	if status, _ := do(t, app, "GET", "/api/admin/rentals/999999999", tok, nil); status != 404 {
		t.Fatalf("unknown but well-formed id: want 404, got %d", status)
	}
	t.Logf("PASS malformed ids -> 400 on every id-bearing route")
}

// TestCarCannotBeMarkedRentedWithoutRental: fleet status must stay consistent
// with the rental table in both directions.
func TestCarCannotBeMarkedRentedWithoutRental(t *testing.T) {
	app := newTestApp(t)
	tok := login(t, app)
	_, cars := doList(t, app, "GET", "/api/admin/cars?status=available&page=1", tok)
	if len(cars) == 0 {
		t.Skip("no idle car available to probe")
	}
	carID := uint(cars[0]["id"].(float64))

	status, _ := do(t, app, "PUT", "/api/admin/cars/"+itoa(carID), tok, map[string]any{"status": "rented"})
	if status != 409 {
		t.Fatalf("marking an idle car rented: want 409, got %d", status)
	}
	status, _ = do(t, app, "PUT", "/api/admin/cars/"+itoa(carID), tok, map[string]any{"status": "reserved"})
	if status != 409 {
		t.Fatalf("marking an idle car reserved: want 409, got %d", status)
	}
	// A legitimate transition is still allowed.
	status, _ = do(t, app, "PUT", "/api/admin/cars/"+itoa(carID), tok, map[string]any{"status": "maintenance"})
	if status != 200 {
		t.Fatalf("marking an idle car for maintenance: want 200, got %d", status)
	}
	if status, _ := do(t, app, "PUT", "/api/admin/cars/"+itoa(carID), tok, map[string]any{"status": "available"}); status != 200 {
		t.Fatalf("restoring availability: want 200, got %d", status)
	}
	t.Logf("PASS car status guard: rented/reserved rejected without a rental, maintenance allowed")
}

// TestRegisterDisabledByDefault: without ALLOW_REGISTRATION the endpoint
// rejects sign-ups with 403, so an unconfigured production never opens
// account creation by accident.
func TestRegisterDisabledByDefault(t *testing.T) {
	app := newTestApp(t)
	status, _ := do(t, app, "POST", "/api/auth/register", "", map[string]any{
		"name": "Should Not Work", "email": "blocked@example.com", "password": "password123",
	})
	if status != 403 {
		t.Fatalf("register disabled: want 403, got %d", status)
	}
	t.Logf("PASS POST /api/auth/register (disabled) -> 403")
}

// TestRegisterFlow: with registration enabled, a new operator account can be
// created and signed in immediately.
func TestRegisterFlow(t *testing.T) {
	app := newTestApp(t)
	os.Setenv("ALLOW_REGISTRATION", "true")
	defer os.Unsetenv("ALLOW_REGISTRATION")

	email := "new-operator-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@example.com"
	status, body := do(t, app, "POST", "/api/auth/register", "", map[string]any{
		"name": "New Operator", "email": email, "password": "password123",
	})
	if status != 201 {
		t.Fatalf("register: want 201, got %d (%v)", status, body)
	}
	tok, _ := body["token"].(string)
	if tok == "" {
		t.Fatalf("register: expected session token, got %v", body)
	}

	// The registered user must land on the console.
	status, profile := do(t, app, "GET", "/api/admin/me", tok, nil)
	if status != 200 {
		t.Fatalf("profile after register: want 200, got %d (%v)", status, profile)
	}
	if role, _ := profile["role"].(string); role != "operator" {
		t.Fatalf("registered role: want operator, got %q", role)
	}

	// Duplicate registration must conflict.
	status, _ = do(t, app, "POST", "/api/auth/register", "", map[string]any{
		"name": "Again", "email": email, "password": "password123",
	})
	if status != 409 {
		t.Fatalf("duplicate register: want 409, got %d", status)
	}
	t.Logf("PASS register flow: create operator, profile role=operator, duplicate -> 409")
}

// TestPasswordResetFlow exercises the full demo-mode reset lifecycle against
// a freshly registered account: request -> single-use token -> set password
// -> sign in with the new password -> old token is dead.
func TestPasswordResetFlow(t *testing.T) {
	app := newTestApp(t)
	os.Setenv("ALLOW_REGISTRATION", "true")
	defer os.Unsetenv("ALLOW_REGISTRATION")

	email := "reset-user-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@example.com"
	status, _ := do(t, app, "POST", "/api/auth/register", "", map[string]any{
		"name": "Reset Tester", "email": email, "password": "old-password-1",
	})
	if status != 201 {
		t.Fatalf("register for reset: want 201, got %d", status)
	}

	// Forgot-password returns a demo-mode reset link for a known address.
	status, forgot := do(t, app, "POST", "/api/auth/forgot-password", "", map[string]any{
		"email": email,
	})
	if status != 200 {
		t.Fatalf("forgot-password: want 200, got %d (%v)", status, forgot)
	}
	resetURL, _ := forgot["reset_url"].(string)
	if forgot["demo"] != true || resetURL == "" {
		t.Fatalf("expected demo-mode reset_url, got %v", forgot)
	}

	// Pull the token out of the returned URL.
	token := ""
	if idx := strings.Index(resetURL, "token="); idx >= 0 {
		token = resetURL[idx+len("token="):]
	}
	if token == "" {
		t.Fatalf("could not parse token from %q", resetURL)
	}

	// Unknown address still returns the same success shape (no token leak).
	status, unknown := do(t, app, "POST", "/api/auth/forgot-password", "", map[string]any{
		"email": "nobody-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@example.com",
	})
	if status != 200 {
		t.Fatalf("forgot-password unknown: want 200, got %d", status)
	}
	if _, leaked := unknown["reset_url"]; leaked {
		t.Fatalf("unknown address must not return a reset_url: %v", unknown)
	}

	// Consume the token with a new password.
	status, reset := do(t, app, "POST", "/api/auth/reset-password", "", map[string]any{
		"token": token, "password": "brand-new-pass-9",
	})
	if status != 200 {
		t.Fatalf("reset-password: want 200, got %d (%v)", status, reset)
	}

	// New password works.
	status, _ = do(t, app, "POST", "/api/auth/login", "", map[string]any{
		"email": email, "password": "brand-new-pass-9",
	})
	if status != 200 {
		t.Fatalf("login with new password: want 200, got %d", status)
	}
	// Old password must be dead.
	status, _ = do(t, app, "POST", "/api/auth/login", "", map[string]any{
		"email": email, "password": "old-password-1",
	})
	if status != 401 {
		t.Fatalf("login with old password: want 401, got %d", status)
	}
	// Token is single-use.
	status, _ = do(t, app, "POST", "/api/auth/reset-password", "", map[string]any{
		"token": token, "password": "another-pass-0",
	})
	if status != 400 {
		t.Fatalf("reuse reset token: want 400, got %d", status)
	}
	t.Logf("PASS password reset: demo link issued, token consumed once, old password invalidated")
}

// TestOperatorRoleIsRestricted: the operator role can browse the console but
// cannot hit delete endpoints, which are gated behind RequireAdmin.
func TestOperatorRoleIsRestricted(t *testing.T) {
	app := newTestApp(t)
	operatorToken := loginAs(t, app, "demo@rental.dev", "demo123")

	// Operator can read the dashboard.
	status, _ := do(t, app, "GET", "/api/admin/dashboard/stats", operatorToken, nil)
	if status != 200 {
		t.Fatalf("operator dashboard: want 200, got %d", status)
	}

	// Operator can list cars (read access is allowed).
	status, cars := doList(t, app, "GET", "/api/admin/cars", operatorToken)
	if status != 200 || len(cars) == 0 {
		t.Fatalf("operator list cars: want 200 with cars, got %d/%d", status, len(cars))
	}

	// Operator cannot delete a car — admin-only.
	carID := uint(cars[0]["id"].(float64))
	status, _ = do(t, app, "DELETE", "/api/admin/cars/"+itoa(carID), operatorToken, nil)
	if status != 403 {
		t.Fatalf("operator delete car: want 403, got %d", status)
	}

	// Operator cannot delete cash-flow entries either.
	status, _ = do(t, app, "DELETE", "/api/admin/cashflow/1", operatorToken, nil)
	if status != 403 {
		t.Fatalf("operator delete cashflow: want 403, got %d", status)
	}

	// The admin account still can (against a manual entry if one exists).
	adminToken := login(t, app)
	status, created := do(t, app, "POST", "/api/admin/cashflow", adminToken, map[string]any{
		"type": "out", "category": "fuel", "amount": 10.0, "description": "RBAC probe",
	})
	if status != 201 {
		t.Fatalf("admin create cashflow: want 201, got %d (%v)", status, created)
	}
	entryID := uint(created["id"].(float64))
	status, _ = do(t, app, "DELETE", "/api/admin/cashflow/"+itoa(entryID), adminToken, nil)
	if status != 200 {
		t.Fatalf("admin delete cashflow: want 200, got %d", status)
	}
	t.Logf("PASS RBAC: operator blocked on DELETE (403), admin allowed (200)")
}
