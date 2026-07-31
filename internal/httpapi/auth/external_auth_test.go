package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"

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
