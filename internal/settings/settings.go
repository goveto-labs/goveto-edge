// Package settings reads runtime-modifiable application settings.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

const (
	RegistrationEnabledKey = "auth.registration.enabled"
	CaptchaKey             = "auth.captcha"
	RequireTOTPKey         = "auth.totp.required"
	InstanceInitializedKey = "instance.initialized"
	InstanceOwnerUserIDKey = "instance.owner_user_id"
	AgentGatewayAddressKey = "agent.gateway.public_address"
	HTTPProxyKey           = "http.proxy"
	LocalLoginEnabledKey   = "auth.local_login.enabled"
	AuthProvidersKey       = "auth.external_providers"
)

const agentGatewayAddressDescription = "Public host and port used by edge nodes to reach the agent gateway"

const (
	httpProxyDescription     = "Client IP forwarding headers used by the control plane"
	localLoginDescription    = "Whether email and password login is available"
	authProvidersDescription = "OAuth 2.0 and OpenID Connect login providers"
)

var DefaultClientIPHeaders = []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"}

type SecretCipher interface {
	EncryptScoped(scope, value string) (string, error)
	DecryptScoped(scope, value string) (string, error)
}

type HTTPProxyConfig struct {
	TrustAll        bool     `json:"trust_all"`
	ClientIPHeaders []string `json:"client_ip_headers"`
}

type AuthProviderType string

const (
	AuthProviderOIDC   AuthProviderType = "OIDC"
	AuthProviderOAuth2 AuthProviderType = "OAUTH2"
)

type AuthProviderConfig struct {
	ID                string           `json:"id"`
	Type              AuthProviderType `json:"type"`
	Preset            string           `json:"preset"`
	Enabled           bool             `json:"enabled"`
	ProviderName      string           `json:"provider_name"`
	IssuerURL         string           `json:"issuer_url"`
	AuthorizationURL  string           `json:"authorization_url"`
	TokenURL          string           `json:"token_url"`
	UserInfoURL       string           `json:"user_info_url"`
	EmailURL          string           `json:"email_url"`
	ClientID          string           `json:"client_id"`
	ClientSecret      string           `json:"-"`
	RedirectURL       string           `json:"redirect_url"`
	Scopes            []string         `json:"scopes"`
	AutoCreateUsers   bool             `json:"auto_create_users"`
	SecretConfigured  bool             `json:"-"`
	ClientSecretValue string           `json:"client_secret_encrypted,omitempty"`
}

type Store struct {
	db       *client.Client
	recorder audit.Recorder
}

func New(db *client.Client, recorder audit.Recorder) *Store {
	return &Store{db: db, recorder: recorder}
}

func (s *Store) Get(ctx context.Context, key string, target any) (bool, error) {
	setting, err := s.db.DynamicSetting.FindUnique(ctx, query.DynamicSetting.Key.Equals(key))
	if err != nil {
		return false, fmt.Errorf("read setting %q: %w", key, err)
	}
	if setting == nil {
		return false, nil
	}
	if err := json.Unmarshal(setting.ValueJson, target); err != nil {
		return false, fmt.Errorf("decode setting %q: %w", key, err)
	}
	return true, nil
}

func (s *Store) Set(ctx context.Context, key string, value any, description string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode setting %q: %w", key, err)
	}
	previous, err := s.db.DynamicSetting.FindUnique(ctx, query.DynamicSetting.Key.Equals(key))
	if err != nil {
		return fmt.Errorf("read setting %q before update: %w", key, err)
	}
	now := time.Now().UTC()
	_, err = s.db.DynamicSetting.UpsertOne(
		ctx,
		query.DynamicSetting.Key.Equals(key),
		[]query.DynamicSettingSetClause{
			query.DynamicSetting.Key.Set(key),
			query.DynamicSetting.ValueJson.Set(encoded),
			query.DynamicSetting.Description.Set(description),
			query.DynamicSetting.UpdatedAt.Set(now),
		},
		[]query.DynamicSettingSetClause{
			query.DynamicSetting.ValueJson.Set(encoded),
			query.DynamicSetting.Description.Set(description),
			query.DynamicSetting.UpdatedAt.Set(now),
		},
	)
	if s.recorder != nil {
		var before any
		if previous != nil {
			before = map[string]any{"value": previous.ValueJson, "description": previous.Description}
		}
		audit.RecordContext(
			ctx, s.recorder, "system_setting.update", "dynamic_setting", key,
			before, map[string]any{"value": json.RawMessage(encoded), "description": description}, err,
		)
	}
	if err != nil {
		return fmt.Errorf("write setting %q: %w", key, err)
	}
	return nil
}

func (s *Store) Initialized(ctx context.Context) (bool, error) {
	var initialized bool
	found, err := s.Get(ctx, InstanceInitializedKey, &initialized)
	return found && initialized, err
}

func (s *Store) RegistrationEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	found, err := s.Get(ctx, RegistrationEnabledKey, &enabled)
	return found && enabled, err
}

func (s *Store) RequireTOTP(ctx context.Context) (bool, error) {
	var required bool
	found, err := s.Get(ctx, RequireTOTPKey, &required)
	return found && required, err
}

func (s *Store) SetRequireTOTP(ctx context.Context, required bool) error {
	return s.Set(ctx, RequireTOTPKey, required, "Require active users to enroll time-based one-time passwords")
}

func (s *Store) LocalLoginEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	found, err := s.Get(ctx, LocalLoginEnabledKey, &enabled)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	return enabled, nil
}

func (s *Store) SetLocalLoginEnabled(ctx context.Context, enabled bool) error {
	return s.Set(ctx, LocalLoginEnabledKey, enabled, localLoginDescription)
}

func (s *Store) HTTPProxy(ctx context.Context) (HTTPProxyConfig, bool, error) {
	config := HTTPProxyConfig{ClientIPHeaders: append([]string(nil), DefaultClientIPHeaders...)}
	found, err := s.Get(ctx, HTTPProxyKey, &config)
	if err != nil {
		return HTTPProxyConfig{}, false, err
	}
	if err := config.NormalizeAndValidate(); err != nil {
		return HTTPProxyConfig{}, found, fmt.Errorf("stored HTTP proxy setting is invalid: %w", err)
	}
	return config, found, nil
}

func (s *Store) SetHTTPProxy(ctx context.Context, config HTTPProxyConfig) error {
	if err := config.NormalizeAndValidate(); err != nil {
		return err
	}
	return s.Set(ctx, HTTPProxyKey, config, httpProxyDescription)
}

func (c *HTTPProxyConfig) NormalizeAndValidate() error {
	headers := make([]string, 0, len(c.ClientIPHeaders))
	for _, raw := range c.ClientIPHeaders {
		value := http.CanonicalHeaderKey(strings.TrimSpace(raw))
		if value == "" || strings.ContainsAny(value, " \t\r\n:") {
			return fmt.Errorf("invalid client IP header %q", raw)
		}
		if !slices.Contains(headers, value) {
			headers = append(headers, value)
		}
	}
	if len(headers) == 0 {
		return errors.New("at least one client IP header is required")
	}
	if len(headers) > 16 {
		return errors.New("HTTP proxy setting contains too many entries")
	}
	c.ClientIPHeaders = headers
	return nil
}

func (s *Store) AuthProviders(ctx context.Context, cipher SecretCipher) ([]AuthProviderConfig, bool, error) {
	var providers []AuthProviderConfig
	found, err := s.Get(ctx, AuthProvidersKey, &providers)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return []AuthProviderConfig{}, false, nil
	}
	for index := range providers {
		provider := &providers[index]
		provider.SecretConfigured = provider.ClientSecretValue != ""
		if provider.ClientSecretValue != "" {
			if cipher == nil {
				return nil, true, errors.New("authentication provider secret cipher is unavailable")
			}
			provider.ClientSecret, err = cipher.DecryptScoped(authProviderSecretScope(provider.ID), provider.ClientSecretValue)
			if err != nil {
				return nil, true, fmt.Errorf("decrypt authentication provider %q client secret: %w", provider.ProviderName, err)
			}
		}
		if err = provider.NormalizeAndValidate(); err != nil {
			return nil, true, fmt.Errorf("stored authentication provider is invalid: %w", err)
		}
	}
	return providers, true, nil
}

func (s *Store) SetAuthProviders(ctx context.Context, providers []AuthProviderConfig, cipher SecretCipher) error {
	existing, _, err := s.AuthProviders(ctx, cipher)
	if err != nil {
		return err
	}
	existingByID := make(map[string]AuthProviderConfig, len(existing))
	for _, provider := range existing {
		existingByID[provider.ID] = provider
	}

	stored := make([]AuthProviderConfig, len(providers))
	seenIDs := make(map[string]bool, len(providers))
	seenNames := make(map[string]bool, len(providers))
	for index, input := range providers {
		input.ID = strings.TrimSpace(input.ID)
		if input.ID == "" {
			return errors.New("authentication provider ID is required")
		}
		if !validProviderID(input.ID) || seenIDs[input.ID] {
			return fmt.Errorf("authentication provider ID %q is invalid or duplicated", input.ID)
		}
		seenIDs[input.ID] = true
		newSecret := input.ClientSecret != ""
		previous, existed := existingByID[input.ID]
		if !newSecret && existed {
			input.ClientSecret = previous.ClientSecret
			input.ClientSecretValue = previous.ClientSecretValue
		}
		if err = input.NormalizeAndValidate(); err != nil {
			return fmt.Errorf("authentication provider %q: %w", input.ProviderName, err)
		}
		nameKey := strings.ToLower(input.ProviderName)
		if seenNames[nameKey] {
			return fmt.Errorf("authentication provider name %q is duplicated", input.ProviderName)
		}
		seenNames[nameKey] = true

		if newSecret {
			if cipher == nil {
				return errors.New("authentication provider secret cipher is unavailable")
			}
			input.ClientSecretValue, err = cipher.EncryptScoped(authProviderSecretScope(input.ID), input.ClientSecret)
			if err != nil {
				return fmt.Errorf("encrypt authentication provider %q client secret: %w", input.ProviderName, err)
			}
		} else if !existed {
			input.ClientSecretValue = ""
		}
		input.ClientSecret = ""
		input.SecretConfigured = false
		stored[index] = input
	}
	if len(stored) > 20 {
		return errors.New("at most 20 authentication providers may be configured")
	}
	return s.Set(ctx, AuthProvidersKey, stored, authProvidersDescription)
}

func authProviderSecretScope(id string) string {
	return "settings:auth.provider:" + id + ":client_secret"
}

func validProviderID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func (c *AuthProviderConfig) NormalizeAndValidate() error {
	c.ID = strings.TrimSpace(c.ID)
	if !validProviderID(c.ID) {
		return errors.New("provider ID is invalid")
	}
	c.Type = AuthProviderType(strings.ToUpper(strings.TrimSpace(string(c.Type))))
	if c.Type == "" {
		c.Type = AuthProviderOIDC
	}
	c.Preset = strings.ToUpper(strings.TrimSpace(c.Preset))
	if c.Preset == "" {
		c.Preset = "CUSTOM_OIDC"
	}
	c.ProviderName = strings.TrimSpace(c.ProviderName)
	c.IssuerURL = strings.TrimRight(strings.TrimSpace(c.IssuerURL), "/")
	c.AuthorizationURL = strings.TrimSpace(c.AuthorizationURL)
	c.TokenURL = strings.TrimSpace(c.TokenURL)
	c.UserInfoURL = strings.TrimSpace(c.UserInfoURL)
	c.EmailURL = strings.TrimSpace(c.EmailURL)
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.RedirectURL = strings.TrimSpace(c.RedirectURL)
	if c.ProviderName == "" {
		c.ProviderName = "Single sign-on"
	}
	if len(c.ProviderName) > 100 || len(c.Preset) > 64 || len(c.ClientID) > 2048 {
		return errors.New("provider name, preset, or client ID is too long")
	}
	scopes := make([]string, 0, len(c.Scopes)+3)
	if c.Type == AuthProviderOIDC {
		scopes = append(scopes, "openid", "email", "profile")
	}
	for _, scope := range c.Scopes {
		value := strings.TrimSpace(scope)
		if len(value) > 128 {
			return errors.New("provider scope is too long")
		}
		if value != "" && !slices.Contains(scopes, value) {
			scopes = append(scopes, value)
		}
	}
	c.Scopes = scopes
	if len(c.Scopes) > 32 {
		return errors.New("provider contains too many scopes")
	}
	if c.Type != AuthProviderOIDC && c.Type != AuthProviderOAuth2 {
		return fmt.Errorf("unsupported provider type %q", c.Type)
	}
	if !c.Enabled {
		return nil
	}
	if c.ClientID == "" || strings.TrimSpace(c.ClientSecret) == "" || c.RedirectURL == "" {
		return errors.New("enabled provider requires a client ID, client secret, and redirect URL")
	}
	urls := map[string]string{"redirect URL": c.RedirectURL}
	if c.Type == AuthProviderOIDC {
		if c.IssuerURL == "" {
			return errors.New("enabled OIDC provider requires an issuer URL")
		}
		urls["issuer URL"] = c.IssuerURL
	} else {
		if c.AuthorizationURL == "" || c.TokenURL == "" || c.UserInfoURL == "" {
			return errors.New("enabled OAuth 2.0 provider requires authorization, token, and user info URLs")
		}
		urls["authorization URL"] = c.AuthorizationURL
		urls["token URL"] = c.TokenURL
		urls["user info URL"] = c.UserInfoURL
		if c.EmailURL != "" {
			urls["email URL"] = c.EmailURL
		}
	}
	for name, raw := range urls {
		if err := validateAuthenticationURL(name, raw); err != nil {
			return err
		}
	}
	return nil
}

func validateAuthenticationURL(name, raw string) error {
	if len(raw) > 4096 {
		return fmt.Errorf("provider %s is too long", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("provider %s must be an absolute HTTP or HTTPS URL", name)
	}
	if parsed.Scheme != "https" {
		ip := net.ParseIP(parsed.Hostname())
		if parsed.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("provider %s must use HTTPS", name)
		}
	}
	return nil
}

func (s *Store) InstanceOwnerUserID(ctx context.Context) (string, bool, error) {
	var userID string
	found, err := s.Get(ctx, InstanceOwnerUserIDKey, &userID)
	return userID, found, err
}

func (s *Store) IsInstanceOwner(ctx context.Context, userID string) (bool, error) {
	ownerID, found, err := s.InstanceOwnerUserID(ctx)
	return found && ownerID != "" && ownerID == userID, err
}

func (s *Store) AgentGatewayPublicAddress(ctx context.Context) (string, bool, error) {
	var address string
	found, err := s.Get(ctx, AgentGatewayAddressKey, &address)
	if err != nil || !found {
		return "", found, err
	}
	normalized, err := ValidateAgentGatewayPublicAddress(address)
	if err != nil {
		return "", true, fmt.Errorf("stored agent gateway public address is invalid: %w", err)
	}
	return normalized, true, nil
}

func (s *Store) SetAgentGatewayPublicAddress(ctx context.Context, address string) error {
	normalized, err := ValidateAgentGatewayPublicAddress(address)
	if err != nil {
		return err
	}
	return s.Set(ctx, AgentGatewayAddressKey, normalized, agentGatewayAddressDescription)
}

func ValidateAgentGatewayPublicAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", fmt.Errorf("agent gateway public address is required")
	}
	if strings.ContainsAny(address, "/ \t\r\n") {
		return "", fmt.Errorf("agent gateway public address must contain only a host and port")
	}

	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return "", fmt.Errorf("agent gateway public address must use host:port format")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("agent gateway public address port must be between 1 and 65535")
	}

	if net.ParseIP(host) == nil {
		host = strings.ToLower(host)
		if !validHostname(host) {
			return "", fmt.Errorf("agent gateway public address host is invalid")
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func validHostname(host string) bool {
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

type CaptchaConfig struct {
	Provider  string `json:"provider"`
	SecretKey string `json:"secret_key"`
	SiteKey   string `json:"site_key"`
}

func (s *Store) Captcha(ctx context.Context) (CaptchaConfig, bool, error) {
	var config CaptchaConfig
	found, err := s.Get(ctx, CaptchaKey, &config)
	return config, found, err
}
