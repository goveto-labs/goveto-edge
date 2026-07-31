package adminsettings

import (
	"encoding/json"
	"strings"
	"testing"

	"goveto-edge/internal/settings"
)

func TestAdminSettingsResponseDoesNotExposeOIDCSecret(t *testing.T) {
	encoded, err := json.Marshal(response{
		Authentication: authenticationResponse{Providers: []authenticationProviderResponse{{ClientSecretConfigured: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"client_secret":`) {
		t.Fatalf("OIDC client secret field exposed in response: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"client_secret_configured":true`) {
		t.Fatalf("OIDC secret configuration state missing from response: %s", encoded)
	}
}

func TestOIDCRuntimeEqualIgnoresStorageMetadata(t *testing.T) {
	left := settings.AuthProviderConfig{
		ID: "provider-1", Type: settings.AuthProviderOIDC,
		Enabled: true, ProviderName: "SSO", IssuerURL: "https://id.example.com",
		ClientID: "client", ClientSecret: "secret", ClientSecretValue: "ciphertext-a",
		RedirectURL: "https://control.example.com/callback", Scopes: []string{"openid", "email"},
	}
	right := left
	right.SecretConfigured = true
	right.ClientSecretValue = "ciphertext-b"
	if !authProviderRuntimeEqual(left, right) {
		t.Fatal("storage-only OIDC metadata was treated as a runtime configuration change")
	}
	right.ClientID = "other-client"
	if authProviderRuntimeEqual(left, right) {
		t.Fatal("OIDC client ID change was ignored")
	}
}

func TestBuildProviderConfigsPreservesExistingSecret(t *testing.T) {
	current := []settings.AuthProviderConfig{{
		ID: "provider-1", Type: settings.AuthProviderOIDC, Enabled: true,
		ProviderName: "Google", IssuerURL: "https://accounts.google.com",
		ClientID: "client", ClientSecret: "existing-secret",
		RedirectURL: "https://control.example.com/callback", Scopes: []string{"openid", "email", "profile"},
	}}
	inputs := []authenticationProviderRequest{{
		ID: "provider-1", Type: settings.AuthProviderOIDC, Enabled: true,
		ProviderName: "Google", IssuerURL: "https://accounts.google.com",
		ClientID: "client", RedirectURL: "https://control.example.com/callback",
	}}
	runtime, stored, err := buildProviderConfigs(inputs, current)
	if err != nil {
		t.Fatal(err)
	}
	if runtime[0].ClientSecret != "existing-secret" {
		t.Fatal("existing secret was not available for validation")
	}
	if stored[0].ClientSecret != "" {
		t.Fatal("unchanged secret was included in the stored provider request")
	}
}
