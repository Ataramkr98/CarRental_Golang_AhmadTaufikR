package handlers

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	"car-rental-backend/middleware"
	"car-rental-backend/services"
)

// checkoutHeader is the header a guest browser echoes back on its payment
// calls. It is the only credential a checkout has, so it is never logged and
// never stored in a cookie.
const checkoutHeader = "x-checkout-token"

// sendCSV writes a downloadable CSV response.
func sendCSV(c fiber.Ctx, filename, body string) error {
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, filename))
	return c.SendString(body)
}

// authorizeRental guards the guest checkout routes.
//
// A console operator is already authenticated by the route middleware, so any
// valid bearer token passes. An anonymous guest must present the one-time
// checkout token issued with the booking — that is what stops a third party
// from paying, cancelling or snooping on someone else's reservation by guessing
// a sequential rental id.
func authorizeRental(c fiber.Ctx, id uint) error {
	if middleware.HasConsoleSession(c) {
		return nil
	}
	return services.VerifyCheckoutToken(id, c.Get(checkoutHeader))
}

// RentalStatus exposes a sanitised lifecycle snapshot for the checkout page
// to poll while a payment is pending.
func RentalStatus(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	if err := authorizeRental(c, id); err != nil {
		return respondError(c, err)
	}
	status, err := services.GetRentalStatus(id)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, status, "Booking status retrieved")
}

// CreateInvoice reserves the car and creates (or safely reuses) an invoice.
func CreateInvoice(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	if err := authorizeRental(c, id); err != nil {
		return respondError(c, err)
	}
	invoice, err := services.CreateInvoice(id)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, invoice, "Invoice ready")
}

// MockPay simulates a successful payment. It is only available in mock mode.
func MockPay(c fiber.Ctx) error {
	if !services.MockPaymentsEnabled() {
		return respondError(c, services.Forbidden("simulated payments are disabled in live mode"))
	}
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	if err := authorizeRental(c, id); err != nil {
		return respondError(c, err)
	}
	if err := services.MarkRentalPaid(id, 0); err != nil {
		return respondError(c, err)
	}

	status, err := services.GetRentalStatus(id)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, fiber.Map{
		"rental": fiber.Map{
			"id": status.ID, "status": status.Status,
			"payment_status": status.PaymentStatus, "paid_at": status.PaidAt,
		},
	}, "Payment received — the operator can now hand over the car")
}

// XenditWebhook receives invoice callbacks from Xendit.
func XenditWebhook(c fiber.Ctx) error {
	if err := services.VerifyCallbackToken(c.Get("x-callback-token")); err != nil {
		return respondError(c, err)
	}

	var payload struct {
		ExternalID string  `json:"external_id"`
		Status     string  `json:"status"`
		PaidAmount float64 `json:"paid_amount"`
	}
	if err := c.Bind().Body(&payload); err != nil {
		return respondInvalid(c, "invalid payload")
	}

	if err := services.HandleWebhook(payload.ExternalID, payload.Status, payload.PaidAmount); err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, fiber.Map{"received": true}, "Webhook processed")
}
