package adminsettings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
)

type testGatewayAddressAuthority struct {
	transactionStarted *bool
	prepared           bool
	published          string
}

func (a *testGatewayAddressAuthority) PrepareGatewayAddressUpdate(address string) (func(), error) {
	if *a.transactionStarted {
		return nil, errors.New("authority prepared after transaction started")
	}
	a.prepared = true
	return func() { a.published = address }, nil
}

type testGatewayAddressNotifier struct {
	transactionStarted *bool
	err                error
	address            string
}

func (n *testGatewayAddressNotifier) NotifyAuthorityUpdateTx(_ context.Context, _ *client.Client, address string) error {
	if !*n.transactionStarted {
		return errors.New("notification ran outside transaction")
	}
	n.address = address
	return n.err
}

func TestPersistAdminSettingsUpdatePublishesOnlyAfterSuccessfulCommit(t *testing.T) {
	for _, test := range []struct {
		name          string
		notifyErr     error
		wantPublished string
	}{
		{name: "commit", wantPublished: "new.example.com:9443"},
		{name: "rollback", notifyErr: errors.New("notify failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			transactionStarted := false
			authority := &testGatewayAddressAuthority{transactionStarted: &transactionStarted}
			notifier := &testGatewayAddressNotifier{transactionStarted: &transactionStarted, err: test.notifyErr}
			apply := func(_ context.Context, _ *settings.PreparedAdminSettingsUpdate, hook func(*client.Client) error) error {
				transactionStarted = true
				if hook != nil {
					return hook(nil)
				}
				return nil
			}
			err := persistAdminSettingsUpdate(
				context.Background(), nil, true, "new.example.com:9443", authority, notifier, apply,
			)
			if !errors.Is(err, test.notifyErr) {
				t.Fatalf("persistAdminSettingsUpdate() error = %v, want %v", err, test.notifyErr)
			}
			if !authority.prepared || notifier.address != "new.example.com:9443" {
				t.Fatalf("prepare/notify missing: authority=%+v notifier=%+v", authority, notifier)
			}
			if authority.published != test.wantPublished {
				t.Fatalf("published address = %q, want %q", authority.published, test.wantPublished)
			}
		})
	}
}

func TestAdminSettingsResponseDoesNotExposeOIDCSecret(t *testing.T) {
	encoded, err := json.Marshal(response{
		Authentication: authenticationResponse{
			Registration: registrationResponse{SecretConfigured: true},
			Providers:    []authenticationProviderResponse{{ClientSecretConfigured: true}},
		},
		JobRetention: settings.DefaultJobRetention,
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
	if strings.Contains(string(encoded), `"captcha_secret":`) || !strings.Contains(string(encoded), `"captcha_secret_configured":true`) {
		t.Fatalf("CAPTCHA secret response is unsafe: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"job_retention":{"history_days":90,"versions_per_site":20}`) {
		t.Fatalf("job retention setting missing from response: %s", encoded)
	}
}

func TestBuildCaptchaConfigDoesNotReuseSecretAcrossProviders(t *testing.T) {
	current := settings.CaptchaConfig{
		Provider: settings.CaptchaProviderCloudflare, SiteKey: "old", SecretKey: "secret", SecretConfigured: true,
	}
	_, err := buildCaptchaConfig(registrationRequest{
		Enabled: true, CaptchaProvider: settings.CaptchaProviderRecaptcha, CaptchaSiteKey: "new",
	}, current)
	if err == nil {
		t.Fatal("provider change reused the existing CAPTCHA secret")
	}
	config, err := buildCaptchaConfig(registrationRequest{
		Enabled: true, CaptchaProvider: settings.CaptchaProviderRecaptcha,
		CaptchaSiteKey: "new", CaptchaSecret: "replacement",
	}, current)
	if err != nil || config.SecretKey != "replacement" {
		t.Fatalf("provider change with replacement secret failed: %#v, %v", config, err)
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
