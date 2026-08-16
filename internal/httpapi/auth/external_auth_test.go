package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"golang.org/x/oauth2"

	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/settings"
)

func TestValidReturnPath(t *testing.T) {
	tests := map[string]string{
		"":                         "/",
		"jobs":                     "/",
		"//attacker.example/path":  "/",
		`/\attacker.example/path`:  "/",
		"https://attacker.example": "/",
		" /jobs?page=2 ":           "/jobs?page=2",
	}
	for input, want := range tests {
		if got := validReturnPath(input); got != want {
			t.Fatalf("validReturnPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExternalAuthCallbackClearsBrowserBindingOnIncompleteResponse(t *testing.T) {
	sessions := authn.NewSessionStore(nil, nil, "session", time.Hour, true)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers/callback", nil)
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(request, recorder)
	sessions.SetExternalAuthBindingCookie(c, "", "binding")
	bindingCookie := recorder.Result().Cookies()[0]
	request.AddCookie(bindingCookie)
	recorder = httptest.NewRecorder()
	c = echo.New().NewContext(request, recorder)

	if err := externalAuthCallback(nil, sessions, nil, nil)(c); err != nil {
		t.Fatal(err)
	}
	response := recorder.Result()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want %d", response.StatusCode, http.StatusFound)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != bindingCookie.Name || cookies[0].MaxAge >= 0 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("cleared browser binding cookie = %#v", cookies)
	}
}

func TestExternalAuthFlowFailureReturnsLinkToSettings(t *testing.T) {
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/callback", nil), recorder)
	if err := externalAuthFlowFailure(c, authn.ExternalAuthState{LinkUserID: "user-1"}, "link failed"); err != nil {
		t.Fatal(err)
	}
	if location := recorder.Header().Get("Location"); location != "/settings?auth_error=link+failed" {
		t.Fatalf("failure redirect = %q", location)
	}
}

func TestExternalAuthStartFailureUsesAPIErrorForLink(t *testing.T) {
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/link", nil), recorder)
	err := externalAuthStartFailure(c, true, http.StatusServiceUnavailable, "unavailable")
	var httpError *echo.HTTPError
	if !errors.As(err, &httpError) || httpError.Code != http.StatusServiceUnavailable || httpError.Message != "unavailable" {
		t.Fatalf("link start error = %#v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("link start wrote response status %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	c = echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/start", nil), recorder)
	if err = externalAuthStartFailure(c, false, http.StatusServiceUnavailable, "unavailable"); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/login?auth_error=unavailable" {
		t.Fatalf("login start response = (%d, %q)", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestExternalAuthLinkRequiresAuthenticatedUser(t *testing.T) {
	e := echo.New()
	e.POST("/providers/:provider_id/link", externalAuthStart(nil, nil, nil, true), authn.RequireUser)
	request := httptest.NewRequest(http.MethodPost, "/providers/provider-1/link", nil)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("link status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestDecideExternalIdentityUser(t *testing.T) {
	tests := []struct {
		name                              string
		identityUser, linkUser, emailUser string
		autoCreate                        bool
		verifiedEmail                     bool
		wantDecision                      externalIdentityDecision
		wantUser                          string
		wantErr                           error
	}{
		{name: "stable identity wins without verified email", identityUser: "user-1", wantDecision: externalIdentityUseExisting, wantUser: "user-1"},
		{name: "identity cannot link to another user", identityUser: "user-1", linkUser: "user-2", wantErr: errExternalIdentityConflict},
		{name: "signed in user explicitly links matching email", linkUser: "user-1", emailUser: "user-1", wantDecision: externalIdentityBindUser, wantUser: "user-1"},
		{name: "signed in user explicitly confirms changed email", linkUser: "user-1", emailUser: "user-2", wantDecision: externalIdentityBindUser, wantUser: "user-1"},
		{name: "new subject requires verified email", autoCreate: true, wantErr: errExternalVerifiedEmail},
		{name: "new subject cannot inherit existing email", emailUser: "user-1", autoCreate: true, verifiedEmail: true, wantErr: errExternalEmailConflict},
		{name: "unknown identity requires auto create", verifiedEmail: true, wantErr: errExternalAccountMissing},
		{name: "unknown identity creates new user", autoCreate: true, verifiedEmail: true, wantDecision: externalIdentityCreateUser},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, userID, err := decideExternalIdentityUser(test.identityUser, test.linkUser, test.emailUser, test.autoCreate, test.verifiedEmail)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("decision error = %v, want %v", err, test.wantErr)
			}
			if err == nil && (decision != test.wantDecision || userID != test.wantUser) {
				t.Fatalf("decision = (%d, %q), want (%d, %q)", decision, userID, test.wantDecision, test.wantUser)
			}
		})
	}
}

func TestExternalIdentityIssuerUsesStableNamespace(t *testing.T) {
	if got := externalIdentityIssuer(settings.AuthProviderConfig{
		ID: "github", Type: settings.AuthProviderOAuth2, UserInfoURL: "https://api.example.com/v1/user",
	}, externalClaims{}); got != "provider:github" {
		t.Fatalf("OAuth issuer = %q", got)
	}
	if got := externalIdentityIssuer(settings.AuthProviderConfig{Type: settings.AuthProviderOIDC}, externalClaims{
		Issuer: "https://issuer.example.com/",
	}); got != "https://issuer.example.com/" {
		t.Fatalf("OIDC issuer = %q", got)
	}
}

func TestExternalIdentityLockKeyIsUnambiguousPostgresText(t *testing.T) {
	first, err := externalIdentityLockKey("provider", "issuer:a", "subject")
	if err != nil {
		t.Fatal(err)
	}
	second, err := externalIdentityLockKey("provider", "issuer", "a:subject")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.ContainsRune(first, '\x00') || strings.ContainsRune(second, '\x00') {
		t.Fatalf("invalid external identity lock keys: %q and %q", first, second)
	}
}

func TestOIDCClaimsKeepVerificationSeparateFromStableIdentity(t *testing.T) {
	claims, err := externalClaimsFromOIDC("https://login.example.com/tenant/v2.0", oidcClaims{
		Subject: "subject-1", Email: "user@example.com", EmailVerified: false, Name: "User",
	})
	if err != nil {
		t.Fatal(err)
	}
	if claims.EmailVerified {
		t.Fatal("unverified OIDC email was promoted to verified")
	}
	claims, err = externalClaimsFromOIDC("https://login.example.com/tenant/v2.0", oidcClaims{
		Subject: "subject-1", Email: "user@example.com", EmailVerified: true, Name: "User",
	})
	if err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != "https://login.example.com/tenant/v2.0" || claims.Subject != "subject-1" {
		t.Fatalf("OIDC identity = %#v", claims)
	}
}

func TestOAuth2ClaimsUsesVerifiedEmailEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization header = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/user":
			_, _ = response.Write([]byte(`{"id":12345,"login":"octocat","email":null}`))
		case "/emails":
			_, _ = response.Write([]byte(`[{"email":"octocat@example.com","primary":true,"verified":true}]`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	config := settings.AuthProviderConfig{
		ID: "github", Type: settings.AuthProviderOAuth2,
		UserInfoURL: server.URL + "/user", EmailURL: server.URL + "/emails",
	}
	oauthConfig := oauth2.Config{}
	claims, err := externalUserClaims(context.Background(), config, nil, oauthConfig, &oauth2.Token{AccessToken: "token", TokenType: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "12345" || claims.Email != "octocat@example.com" || claims.Name != "octocat" {
		t.Fatalf("OAuth 2.0 claims = %#v", claims)
	}
}

func TestOAuth2ClaimsKeepUnverifiedEmailSeparateFromStableIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"sub":"user-1","email":"user@example.com","name":"User"}`))
	}))
	defer server.Close()

	config := settings.AuthProviderConfig{
		ID: "generic", Type: settings.AuthProviderOAuth2, UserInfoURL: server.URL,
	}
	claims, err := externalUserClaims(context.Background(), config, nil, oauth2.Config{}, &oauth2.Token{AccessToken: "token", TokenType: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.Email != "user@example.com" || claims.EmailVerified {
		t.Fatalf("OAuth 2.0 claims = %#v", claims)
	}
	if _, _, err = decideExternalIdentityUser("", "", "", true, claims.EmailVerified); !errors.Is(err, errExternalVerifiedEmail) {
		t.Fatalf("unverified OAuth 2.0 account creation error = %v, want %v", err, errExternalVerifiedEmail)
	}
}
