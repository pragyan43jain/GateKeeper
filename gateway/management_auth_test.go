package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagementQuotaIndependentOfConsumerPolicy(t *testing.T) {
	limiter := &capturingPolicyLimiter{}
	if _, err := allowManagementRequest(context.Background(), limiter, "admin"); err != nil {
		t.Fatal(err)
	}
	if limiter.limit != 120 {
		t.Fatalf("management limit = %d, want 120", limiter.limit)
	}
}

func TestDashboardPasswordRejectsBcryptOverflow(t *testing.T) {
	if _, err := normalizeDashboardCredentials("admin@example.test", strings.Repeat("a", 73)); err == nil {
		t.Fatal("bcrypt-incompatible password accepted")
	}
	if _, err := normalizeDashboardCredentials("admin@example.test", strings.Repeat("a", 72)); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardPasswordHashAndCredentialValidation(t *testing.T) {
	password := "correct-horse-battery-staple"
	hash, err := hashDashboardPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, password) {
		t.Fatal("password hash contains the plaintext password")
	}
	if err := compareDashboardPassword(hash, password); err != nil {
		t.Fatalf("compare valid password: %v", err)
	}
	if compareDashboardPassword(hash, "incorrect-password") == nil {
		t.Fatal("incorrect password was accepted")
	}
	if _, err := normalizeDashboardCredentials("not-an-email", password); err == nil {
		t.Fatal("invalid email was accepted")
	}
	if _, err := normalizeDashboardCredentials("admin@example.test", "short"); err == nil {
		t.Fatal("short password was accepted")
	}
}

func TestManagementAuthRequiresSessionInsteadOfConsumerAPIKey(t *testing.T) {
	auth := NewManagementAuth(nil, "consumer-key", false, nil)
	request := httptest.NewRequest("GET", "/api/keys", nil)
	request.Header.Set("X-API-Key", "consumer-key")
	if _, err := auth.Authenticate(request); err == nil {
		t.Fatal("consumer API key unexpectedly authenticated dashboard management")
	}
}

func TestSessionCookieIsHttpOnlyAndStrict(t *testing.T) {
	auth := NewManagementAuth(nil, "", true, nil)
	response := httptest.NewRecorder()
	auth.SetSessionCookie(response, "token")
	cookie := response.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != 3 || cookie.Path != "/" {
		t.Fatalf("unexpected session cookie flags: %+v", cookie)
	}
}
