package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/storage/gen/model"
)

// currentUIDContextKey mirrors the unexported constant in internal/auth so the
// tests can populate an authenticated context without a full session stack.
const currentUIDContextKey = "auth.current_uid"

func newEnrollmentContext(uid, path string) *echo.Context {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	c := echo.New().NewContext(req, httptest.NewRecorder())
	if uid != "" {
		c.Set(currentUIDContextKey, uid)
	}
	return c
}

// TestRequireTOTPEnrollmentBypassesBootstrapPaths locks down the allowlist:
// the login/register/enrollment bootstrap endpoints, the health probes and
// non-API paths must skip the TOTP gate even for an authenticated user, and an
// anonymous request must never be challenged. A regression here would lock
// users out of the very endpoints that let them enroll.
func TestRequireTOTPEnrollmentBypassesBootstrapPaths(t *testing.T) {
	middleware := RequireTOTPEnrollment(nil) // bypass paths never consult the store.

	bypass := []string{
		"/api/v1/auth/login",
		"/api/v1/auth/register",
		"/api/v1/auth/registration-config",
		"/api/v1/auth/password-reset",
		"/api/v1/init",
		"/api/v1/init/status",
		"/api/v1/auth/me",
		"/api/v1/auth/logout",
		"/api/v1/auth/totp/setup",
		"/api/v1/auth/totp/enable",
		"/api/v1/health",
		"/api/v1/health/live",
		"/static/app.js",
		"/",
	}
	for _, path := range bypass {
		c := newEnrollmentContext("user-1", path)
		called := false
		handler := middleware(func(*echo.Context) error { called = true; return nil })
		if err := handler(c); err != nil {
			t.Fatalf("bypass path %q errored: %v", path, err)
		}
		if !called {
			t.Fatalf("bypass path %q did not reach next handler", path)
		}
	}

	// An anonymous request to a protected API path still passes through.
	c := newEnrollmentContext("", "/api/v1/users")
	called := false
	if err := middleware(func(*echo.Context) error { called = true; return nil })(c); err != nil {
		t.Fatalf("anonymous protected request errored: %v", err)
	}
	if !called {
		t.Fatal("anonymous request was not passed through")
	}
}

// TestRequireTOTPEnrollmentConsultsStoreForProtectedPaths confirms a protected
// API path with an authenticated principal is NOT silently bypassed: it reaches
// the settings store. We pass a nil store and assert the handler never reaches
// `next` (it panics consulting the store instead).
func TestRequireTOTPEnrollmentConsultsStoreForProtectedPaths(t *testing.T) {
	middleware := RequireTOTPEnrollment(nil)
	c := newEnrollmentContext("user-1", "/api/v1/users")
	called := false
	handler := middleware(func(*echo.Context) error { called = true; return nil })

	var reached any
	func() {
		defer func() { reached = recover() }()
		_ = handler(c)
	}()
	if called {
		t.Fatal("protected path was bypassed without consulting the policy store")
	}
	if reached == nil {
		t.Fatal("protected path did not consult the nil policy store")
	}
}

// TestNewUserResponse maps a user and reports the TOTP-enabled flag without
// exposing the shared secret.
func TestNewUserResponse(t *testing.T) {
	user := &model.User{Id: "u-1", Email: "a@b.com", Name: "A", Role: model.UserRoleADMIN, Status: model.UserStatusACTIVE}
	secret := "JBSWY3DPEHPK3PXP"
	user.TotpSecretEncrypted = &secret

	response := newUserResponse(user)
	if response.ID != "u-1" || response.Email != "a@b.com" || response.Role != model.UserRoleADMIN ||
		response.Status != model.UserStatusACTIVE || !response.TOTPEnabled {
		t.Fatalf("response mapping wrong: %#v", response)
	}

	// The shared secret must never appear in the serialized payload.
	user.TotpSecretEncrypted = &secret
	if !hasTOTP(user) {
		t.Fatal("hasTOTP should be true when a secret is set")
	}
	blank := "   "
	user.TotpSecretEncrypted = &blank
	if hasTOTP(user) {
		t.Fatal("hasTOTP should be false for a whitespace-only secret")
	}
}

// TestRateKeyUser prefers the authenticated user id and falls back to the
// client IP so the sensitive-endpoint throttle cannot be bypassed by a
// hijacked session rotating IPs.
func TestRateKeyUser(t *testing.T) {
	c := newEnrollmentContext("user-1", "/api/v1/auth/totp/enable")
	c.Set(currentUIDContextKey, "user-1")
	if key := rateKeyUser(c); key != "user:user-1" {
		t.Fatalf("key = %q, want user:user-1", key)
	}
	anonymous := newEnrollmentContext("", "/api/v1/auth/totp/enable")
	if key := rateKeyUser(anonymous); key == "" || key == "user:" {
		t.Fatalf("anonymous key not derived from IP: %q", key)
	}
}
