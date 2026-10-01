package xendit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client talks to the Xendit Invoice API (https://docs.xendit.co).
//
// When mock mode is enabled (XENDIT_MOCK=true — or automatically when no
// XENDIT_SECRET_KEY is configured) it returns simulated invoices so the full
// payment flow works locally without live credentials. Set XENDIT_MOCK=false
// and provide XENDIT_SECRET_KEY to charge for real.
type Client struct {
	SecretKey string
	BaseURL   string
	Mock      bool
	HTTP      *http.Client
}

// NewClient builds a Client from environment configuration.
func NewClient() *Client {
	secret := os.Getenv("XENDIT_SECRET_KEY")
	mock := os.Getenv("XENDIT_MOCK")
	// default to mock when explicitly requested OR when no secret key is set
	isMock := mock == "true" || mock == "1" || secret == ""
	base := os.Getenv("XENDIT_BASE_URL")
	if base == "" {
		base = "https://api.xendit.co"
	}
	return &Client{
		SecretKey: secret,
		BaseURL:   base,
		Mock:      isMock,
		HTTP:      &http.Client{Timeout: 20 * time.Second},
	}
}

// Invoice is the subset of Xendit invoice fields we use.
type Invoice struct {
	ID         string  `json:"id"`
	ExternalID string  `json:"external_id"`
	InvoiceURL string  `json:"invoice_url"`
	Status     string  `json:"status"`
	Amount     float64 `json:"amount"`
}

// CreateInvoiceRequest is the body for POST /v2/invoices.
type CreateInvoiceRequest struct {
	ExternalID      string  `json:"external_id"`
	Amount          float64 `json:"amount"`
	Description     string  `json:"description"`
	Currency        string  `json:"currency,omitempty"`
	InvoiceDuration int     `json:"invoice_duration,omitempty"` // seconds the invoice stays open
	CustomerEmail   string  `json:"customer_email,omitempty"`
}

func (c *Client) authHeader() string {
	// Xendit uses HTTP Basic auth with the secret key as the username.
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.SecretKey+":"))
}

// CreateInvoice creates a Xendit invoice (or a simulated one in mock mode).
func (c *Client) CreateInvoice(req CreateInvoiceRequest) (*Invoice, error) {
	if c.Mock {
		return &Invoice{
			ID:         "inv-mock-" + req.ExternalID,
			ExternalID: req.ExternalID,
			InvoiceURL: "/mock-pay/" + req.ExternalID, // resolved by the frontend mock-pay button
			Status:     "PENDING",
			Amount:     req.Amount,
		}, nil
	}

	if req.Currency == "" {
		req.Currency = "IDR"
	}
	if req.InvoiceDuration == 0 {
		req.InvoiceDuration = 24 * 3600 // 24 hours
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("xendit: encode invoice request: %w", err)
	}
	httpReq, err := http.NewRequest("POST", c.BaseURL+"/v2/invoices", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", c.authHeader())
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// The response body carries the gateway's own error code, which is the
		// only way to tell a bad key from a rejected payload.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("xendit: create invoice failed with status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	var inv Invoice
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		return nil, err
	}
	return &inv, nil
}
