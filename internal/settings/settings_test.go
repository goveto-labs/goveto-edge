package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateAgentGatewayPublicAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "hostname", input: " Edge.Example.COM:8443 ", want: "edge.example.com:8443"},
		{name: "IPv4", input: "203.0.113.10:9443", want: "203.0.113.10:9443"},
		{name: "IPv6", input: "[2001:db8::1]:8443", want: "[2001:db8::1]:8443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidateAgentGatewayPublicAddress(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("address = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateAgentGatewayPublicAddressRejectsInvalidValues(t *testing.T) {
	values := []string{
		"",
		"edge.example.com",
		"https://edge.example.com:8443",
		"edge.example.com:8443/path",
		"edge.example.com:0",
		"edge.example.com:65536",
		"-edge.example.com:8443",
		"edge_example.com:8443",
		"2001:db8::1:8443",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			if _, err := ValidateAgentGatewayPublicAddress(value); err == nil {
				t.Fatalf("ValidateAgentGatewayPublicAddress(%q) succeeded", value)
			}
		})
	}
}

func TestHTTPProxyConfigNormalizeAndValidate(t *testing.T) {
	config := HTTPProxyConfig{
		TrustAll:        true,
		ClientIPHeaders: []string{"x-forwarded-for", " X-Real-IP ", "X-Forwarded-For"},
	}
	if err := config.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	wantHeaders := []string{"X-Forwarded-For", "X-Real-Ip"}
	if strings.Join(config.ClientIPHeaders, ",") != strings.Join(wantHeaders, ",") {
		t.Fatalf("client IP headers = %#v, want %#v", config.ClientIPHeaders, wantHeaders)
	}
}

func TestHTTPProxyConfigRejectsInvalidValues(t *testing.T) {
	for _, config := range []HTTPProxyConfig{
		{ClientIPHeaders: nil},
		{ClientIPHeaders: []string{"X-Real-IP: injected"}},
	} {
		if err := config.NormalizeAndValidate(); err == nil {
			t.Fatalf("invalid proxy config was accepted: %#v", config)
		}
	}
}

func TestOIDCProviderNormalizeAndValidate(t *testing.T) {
	config := AuthProviderConfig{
		ID: "provider-1", Enabled: true, IssuerURL: " https://id.example.com/ ", ClientID: " client-id ",
		ClientSecret: " secret ", RedirectURL: "https://control.example.com/api/v1/auth/providers/callback",
		Scopes: []string{"groups", "email", " groups "},
	}
	if err := config.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if config.ProviderName != "Single sign-on" || config.IssuerURL != "https://id.example.com" {
		t.Fatalf("normalized OIDC config = %#v", config)
	}
	if got := strings.Join(config.Scopes, ","); got != "openid,email,profile,groups" {
		t.Fatalf("OIDC scopes = %q", got)
	}
}

func TestOIDCProviderValidationAndSecretJSON(t *testing.T) {
	config := AuthProviderConfig{ID: "provider-1", Enabled: true, IssuerURL: "http://id.example.com", ClientID: "id", ClientSecret: "plaintext", RedirectURL: "https://control.example.com/callback"}
	if err := config.NormalizeAndValidate(); err == nil {
		t.Fatal("insecure non-local issuer URL was accepted")
	}
	config.Enabled = false
	config.ClientSecretValue = "ciphertext"
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "plaintext") || !strings.Contains(string(encoded), "ciphertext") {
		t.Fatalf("unexpected OIDC JSON: %s", encoded)
	}
}

func TestOIDCProviderAllowsHTTPOnlyForLoopback(t *testing.T) {
	for _, issuer := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		config := AuthProviderConfig{ID: "provider-1", Enabled: true, IssuerURL: issuer, ClientID: "id", ClientSecret: "secret", RedirectURL: "http://localhost:5173/callback"}
		if err := config.NormalizeAndValidate(); err != nil {
			t.Fatalf("loopback issuer %q rejected: %v", issuer, err)
		}
	}
	config := AuthProviderConfig{ID: "provider-1", Enabled: true, IssuerURL: "http://192.0.2.10", ClientID: "id", ClientSecret: "secret", RedirectURL: "https://control.example.com/callback"}
	if err := config.NormalizeAndValidate(); err == nil {
		t.Fatal("non-loopback HTTP issuer was accepted")
	}
}

func TestOAuth2ProviderNormalizeAndValidate(t *testing.T) {
	config := AuthProviderConfig{
		ID: "github", Type: AuthProviderOAuth2, Enabled: true, ProviderName: "GitHub",
		AuthorizationURL: "https://github.com/login/oauth/authorize",
		TokenURL:         "https://github.com/login/oauth/access_token",
		UserInfoURL:      "https://api.github.com/user", EmailURL: "https://api.github.com/user/emails",
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://control.example.com/callback",
		Scopes: []string{"read:user", "user:email", "read:user"},
	}
	if err := config.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(config.Scopes, ","); got != "read:user,user:email" {
		t.Fatalf("OAuth 2.0 scopes = %q", got)
	}
	if config.IssuerURL != "" {
		t.Fatalf("OAuth 2.0 issuer URL = %q", config.IssuerURL)
	}
}

func TestAuthenticationProviderRejectsInvalidID(t *testing.T) {
	config := AuthProviderConfig{ID: "bad/provider", Type: AuthProviderOIDC}
	if err := config.NormalizeAndValidate(); err == nil {
		t.Fatal("invalid provider ID was accepted")
	}
}
