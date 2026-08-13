package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestExternalAuthCallbackClearsBrowserBindingOnProviderFailure(t *testing.T) {
	sessions := authn.NewSessionStore(nil, nil, "session", time.Hour, true)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers/callback?error=access_denied&state=state-1", nil)
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(request, recorder)
	sessions.SetExternalAuthBindingCookie(c, "state-1", "binding")
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

func TestOAuth2ClaimsRejectsUnverifiedEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"sub":"user-1","email":"user@example.com","name":"User"}`))
	}))
	defer server.Close()

	config := settings.AuthProviderConfig{
		ID: "generic", Type: settings.AuthProviderOAuth2, UserInfoURL: server.URL,
	}
	_, err := externalUserClaims(context.Background(), config, nil, oauth2.Config{}, &oauth2.Token{AccessToken: "token", TokenType: "Bearer"})
	if err == nil {
		t.Fatal("unverified OAuth 2.0 email was accepted")
	}
}
