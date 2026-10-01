package main

import (
	"strings"
	"testing"

	"car-rental-backend/config"
)

// TestJWTSecretIsNeverAPublishedDefault guards the most dangerous kind of
// configuration bug: a signing key that is committed to the repository.
//
// The API used to fall back to the literal "super-secret-change-me" when
// JWT_SECRET was unset, which meant any deployment that forgot the variable was
// signed with a key present in every clone. It now generates a random secret
// instead, and refuses a configured secret shorter than 32 characters. This test
// pins that invariant regardless of how the process happens to be configured.
func TestJWTSecretIsNeverAPublishedDefault(t *testing.T) {
	secret := string(config.JWTSecret())

	if strings.TrimSpace(secret) == "" {
		t.Fatal("JWTSecret() returned an empty secret")
	}
	if len(secret) < 32 {
		t.Fatalf("JWTSecret() returned %d characters; a signing key must be at least 32", len(secret))
	}
	for _, published := range []string{"super-secret-change-me", "change-me", "secret", "jwt-secret"} {
		if secret == published {
			t.Fatalf("JWTSecret() returned the published default %q", published)
		}
	}
}

// TestJWTSecretIsStableWithinTheProcess: resolving twice must return the same
// key, or every signed token would fail to validate on the next request.
func TestJWTSecretIsStableWithinTheProcess(t *testing.T) {
	if string(config.JWTSecret()) != string(config.JWTSecret()) {
		t.Fatal("JWTSecret() returned different values on consecutive calls")
	}
}
