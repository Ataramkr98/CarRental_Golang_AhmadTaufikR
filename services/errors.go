package services

import (
	"errors"
	"net/http"
)

// Kind classifies a domain error so the HTTP layer can translate it into a
// status code without knowing anything about the business rules behind it.
type Kind int

const (
	KindInternal Kind = iota
	KindInvalid
	KindUnauthorized
	KindForbidden
	KindNotFound
	KindConflict
	KindBadGateway
	KindUnavailable
)

// Error is a domain error carrying a safe, user-facing message.
type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string { return e.Message }

// Invalid reports malformed or out-of-range input (HTTP 400).
func Invalid(message string) error { return &Error{Kind: KindInvalid, Message: message} }

// Unauthorized reports failed authentication (HTTP 401).
func Unauthorized(message string) error { return &Error{Kind: KindUnauthorized, Message: message} }

// Forbidden reports an authenticated caller lacking permission (HTTP 403).
func Forbidden(message string) error { return &Error{Kind: KindForbidden, Message: message} }

// NotFound reports a missing entity (HTTP 404).
func NotFound(message string) error { return &Error{Kind: KindNotFound, Message: message} }

// Conflict reports a state that forbids the requested operation (HTTP 409).
func Conflict(message string) error { return &Error{Kind: KindConflict, Message: message} }

// BadGateway reports a failed upstream dependency such as the payment gateway
// (HTTP 502).
func BadGateway(message string) error { return &Error{Kind: KindBadGateway, Message: message} }

// Unavailable reports a dependency that is not configured (HTTP 503).
func Unavailable(message string) error { return &Error{Kind: KindUnavailable, Message: message} }

// StatusCode maps any error to the HTTP status the client should receive.
// Unrecognised errors become 500 so internal details never leak.
func StatusCode(err error) int {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		switch domainErr.Kind {
		case KindInvalid:
			return http.StatusBadRequest
		case KindUnauthorized:
			return http.StatusUnauthorized
		case KindForbidden:
			return http.StatusForbidden
		case KindNotFound:
			return http.StatusNotFound
		case KindConflict:
			return http.StatusConflict
		case KindBadGateway:
			return http.StatusBadGateway
		case KindUnavailable:
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusInternalServerError
}

// PublicMessage returns a message safe to send to the browser. Unexpected
// errors are masked behind a generic string.
func PublicMessage(err error) string {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		return domainErr.Message
	}
	return "internal server error"
}

// IsNotFound reports whether err is a domain NotFound error.
func IsNotFound(err error) bool {
	var domainErr *Error
	return errors.As(err, &domainErr) && domainErr.Kind == KindNotFound
}
