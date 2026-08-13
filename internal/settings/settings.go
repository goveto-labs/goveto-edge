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
	AgentGatewayAddressKey = "agent.gateway.public_address"
	HTTPProxyKey           = "http.proxy"
	LocalLoginEnabledKey   = "auth.local_login.enabled"
	AuthProvidersKey       = "auth.external_providers"
	JobRetentionKey        = "jobs.retention"
)

const agentGatewayAddressDescription = "Public host and port used by edge nodes to reach the agent gateway"

const (
	httpProxyDescription     = "Client IP forwarding headers used by the control plane"
	localLoginDescription    = "Whether email and password login is available"
	authProvidersDescription = "OAuth 2.0 and OpenID Connect login providers"
	captchaDescription       = "CAPTCHA provider used to protect public registration"
	registrationDescription  = "Whether users may create accounts through public registration"
	jobRetentionDescription  = "Retention policy for terminal jobs, executions, and site configuration versions"
)

const (
	CaptchaProviderCloudflare = "cloudflare"
	CaptchaProviderRecaptcha  = "recaptcha"
	captchaSecretScope        = "settings:auth.captcha:secret_key"
)

var DefaultClientIPHeaders = []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"}

type SecretCipher interface {
	EncryptScoped(scope, value string) (string, error)
	DecryptScoped(scope, value string) (string, error)
}

type SecretRewrapper interface {
	RewrapScoped(scope, value string) (string, bool, error)
}

type HTTPProxyConfig struct {
	TrustAll        bool     `json:"trust_all"`
	ClientIPHeaders []string `json:"client_ip_headers"`
}

type JobRetentionConfig struct {
	HistoryDays     int `json:"history_days"`
	VersionsPerSite int `json:"versions_per_site"`
}

var DefaultJobRetention = JobRetentionConfig{HistoryDays: 90, VersionsPerSite: 20}

func (c JobRetentionConfig) Validate() error {
	if c.HistoryDays < 7 || c.HistoryDays > 3650 {
		return errors.New("job history days must be between 7 and 3650")
	}
	if c.VersionsPerSite < 2 || c.VersionsPerSite > 1000 {
		return errors.New("configuration versions per site must be between 2 and 1000")
	}
	return nil
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

func (s *Store) JobRetention(ctx context.Context) (JobRetentionConfig, error) {
	config := DefaultJobRetention
	if _, err := s.Get(ctx, JobRetentionKey, &config); err != nil {
		return JobRetentionConfig{}, err
	}
	if err := config.Validate(); err != nil {
		return JobRetentionConfig{}, fmt.Errorf("stored job retention setting is invalid: %w", err)
	}
	return config, nil
}

func (s *Store) SetJobRetention(ctx context.Context, config JobRetentionConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	return s.Set(ctx, JobRetentionKey, config, jobRetentionDescription)
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

// RewrapAuthProviderSecrets upgrades encrypted client secrets in place while
// preserving provider fields that may have been written by a newer version.
func (s *Store) RewrapAuthProviderSecrets(ctx context.Context, cipher SecretRewrapper) error {
	setting, err := s.db.DynamicSetting.FindUnique(ctx, query.DynamicSetting.Key.Equals(AuthProvidersKey))
	if err != nil {
		return fmt.Errorf("read authentication providers for rewrap: %w", err)
	}
	if setting == nil {
		return nil
	}
	encoded, changed, err := rewrapAuthProviderJSON(setting.ValueJson, cipher)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	_, err = s.db.DynamicSetting.Update().Where(query.DynamicSetting.Key.Equals(AuthProvidersKey)).Set(
		query.DynamicSetting.ValueJson.Set(encoded),
	).Do(ctx)
	if err != nil {
		return fmt.Errorf("persist rewrapped authentication providers: %w", err)
	}
	return nil
}

func rewrapAuthProviderJSON(value json.RawMessage, cipher SecretRewrapper) (json.RawMessage, bool, error) {
	var providers []map[string]json.RawMessage
	if err := json.Unmarshal(value, &providers); err != nil {
		return nil, false, fmt.Errorf("decode authentication providers for rewrap: %w", err)
	}
	changed := false
	for index, provider := range providers {
		var id, encrypted string
		if raw := provider["id"]; raw != nil {
			if err := json.Unmarshal(raw, &id); err != nil {
				return nil, false, fmt.Errorf("decode authentication provider %d ID: %w", index, err)
			}
		}
		if raw := provider["client_secret_encrypted"]; raw != nil {
			if err := json.Unmarshal(raw, &encrypted); err != nil {
				return nil, false, fmt.Errorf("decode authentication provider %q client secret: %w", id, err)
			}
		}
		if encrypted == "" {
			continue
		}
		wrapped, valueChanged, err := cipher.RewrapScoped(authProviderSecretScope(id), encrypted)
		if err != nil {
			return nil, false, fmt.Errorf("rewrap authentication provider %q client secret: %w", id, err)
		}
		if valueChanged {
			provider["client_secret_encrypted"], _ = json.Marshal(wrapped)
			changed = true
		}
	}
	if !changed {
		return value, false, nil
	}
	encoded, err := json.Marshal(providers)
	if err != nil {
		return nil, false, fmt.Errorf("encode rewrapped authentication providers: %w", err)
	}
	return encoded, true, nil
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
	Provider         string `json:"provider"`
	SecretKey        string `json:"-"`
	SiteKey          string `json:"site_key"`
	SecretConfigured bool   `json:"-"`
	secretEncrypted  string
}

type storedCaptchaConfig struct {
	Provider           string `json:"provider"`
	SiteKey            string `json:"site_key"`
	LegacySecretKey    string `json:"secret_key,omitempty"`
	SecretKeyEncrypted string `json:"secret_key_encrypted,omitempty"`
}

func (c *CaptchaConfig) NormalizeAndValidate(requireComplete bool) error {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "turnstile" {
		c.Provider = CaptchaProviderCloudflare
	}
	c.SiteKey = strings.TrimSpace(c.SiteKey)
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	c.SecretConfigured = c.SecretConfigured || c.SecretKey != ""

	if c.Provider != "" && c.Provider != CaptchaProviderCloudflare && c.Provider != CaptchaProviderRecaptcha {
		return fmt.Errorf("unsupported CAPTCHA provider %q", c.Provider)
	}
	if len(c.SiteKey) > 2048 || len(c.SecretKey) > 2048 {
		return errors.New("CAPTCHA keys must not exceed 2048 characters")
	}
	if requireComplete && (c.Provider == "" || c.SiteKey == "" || !c.SecretConfigured) {
		return errors.New("public registration requires a CAPTCHA provider, site key, and secret key")
	}
	return nil
}

func (s *Store) Captcha(ctx context.Context, cipher SecretCipher) (CaptchaConfig, bool, error) {
	var stored storedCaptchaConfig
	found, err := s.Get(ctx, CaptchaKey, &stored)
	if err != nil || !found {
		return CaptchaConfig{}, found, err
	}
	config := CaptchaConfig{
		Provider: stored.Provider, SiteKey: stored.SiteKey,
		SecretConfigured: stored.SecretKeyEncrypted != "" || stored.LegacySecretKey != "",
		secretEncrypted:  stored.SecretKeyEncrypted,
	}
	switch {
	case stored.SecretKeyEncrypted != "":
		if cipher == nil {
			return CaptchaConfig{}, true, errors.New("CAPTCHA secret cipher is unavailable")
		}
		config.SecretKey, err = cipher.DecryptScoped(captchaSecretScope, stored.SecretKeyEncrypted)
		if err != nil {
			return CaptchaConfig{}, true, fmt.Errorf("decrypt CAPTCHA secret key: %w", err)
		}
	case stored.LegacySecretKey != "":
		// Read legacy plaintext long enough for the startup rewrap to migrate it.
		config.SecretKey = stored.LegacySecretKey
	}
	if err = config.NormalizeAndValidate(false); err != nil {
		return CaptchaConfig{}, true, fmt.Errorf("stored CAPTCHA setting is invalid: %w", err)
	}
	return config, true, nil
}

func (s *Store) SetRegistrationConfig(ctx context.Context, enabled bool, config CaptchaConfig, cipher SecretCipher) error {
	newSecret := strings.TrimSpace(config.SecretKey) != ""
	if err := config.NormalizeAndValidate(false); err != nil {
		return err
	}
	config.SecretConfigured = config.SecretKey != ""
	if config.SecretKey == "" {
		current, found, err := s.Captcha(ctx, cipher)
		if err != nil {
			return err
		}
		if found && current.Provider == config.Provider {
			config.SecretKey = current.SecretKey
			config.SecretConfigured = current.SecretConfigured
			config.secretEncrypted = current.secretEncrypted
		}
	}
	if err := config.NormalizeAndValidate(enabled); err != nil {
		return err
	}
	stored := storedCaptchaConfig{Provider: config.Provider, SiteKey: config.SiteKey}
	if !newSecret && config.secretEncrypted != "" {
		stored.SecretKeyEncrypted = config.secretEncrypted
	} else if config.SecretKey != "" {
		if cipher == nil {
			return errors.New("CAPTCHA secret cipher is unavailable")
		}
		encrypted, err := cipher.EncryptScoped(captchaSecretScope, config.SecretKey)
		if err != nil {
			return fmt.Errorf("encrypt CAPTCHA secret key: %w", err)
		}
		stored.SecretKeyEncrypted = encrypted
	}

	// Write the feature gate last when enabling and first when disabling so a
	// partial database failure cannot expose registration without CAPTCHA.
	if !enabled {
		if err := s.Set(ctx, RegistrationEnabledKey, false, registrationDescription); err != nil {
			return err
		}
	}
	if err := s.Set(ctx, CaptchaKey, stored, captchaDescription); err != nil {
		return err
	}
	if enabled {
		return s.Set(ctx, RegistrationEnabledKey, true, registrationDescription)
	}
	return nil
}

type CaptchaSecretCipher interface {
	SecretCipher
	SecretRewrapper
}

// RewrapCaptchaSecret encrypts legacy plaintext settings and rotates existing
// ciphertext while retaining fields written by newer versions.
func (s *Store) RewrapCaptchaSecret(ctx context.Context, cipher CaptchaSecretCipher) error {
	setting, err := s.db.DynamicSetting.FindUnique(ctx, query.DynamicSetting.Key.Equals(CaptchaKey))
	if err != nil {
		return fmt.Errorf("read CAPTCHA setting for rewrap: %w", err)
	}
	if setting == nil {
		return nil
	}
	encoded, changed, err := rewrapCaptchaJSON(setting.ValueJson, cipher)
	if err != nil || !changed {
		return err
	}
	_, err = s.db.DynamicSetting.Update().Where(query.DynamicSetting.Key.Equals(CaptchaKey)).Set(
		query.DynamicSetting.ValueJson.Set(encoded),
	).Do(ctx)
	if err != nil {
		return fmt.Errorf("persist rewrapped CAPTCHA secret: %w", err)
	}
	return nil
}

func rewrapCaptchaJSON(value json.RawMessage, cipher CaptchaSecretCipher) (json.RawMessage, bool, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal(value, &config); err != nil {
		return nil, false, fmt.Errorf("decode CAPTCHA setting for rewrap: %w", err)
	}
	var plaintext, encrypted string
	if raw := config["secret_key"]; raw != nil {
		if err := json.Unmarshal(raw, &plaintext); err != nil {
			return nil, false, fmt.Errorf("decode legacy CAPTCHA secret: %w", err)
		}
	}
	if raw := config["secret_key_encrypted"]; raw != nil {
		if err := json.Unmarshal(raw, &encrypted); err != nil {
			return nil, false, fmt.Errorf("decode encrypted CAPTCHA secret: %w", err)
		}
	}
	if encrypted == "" && plaintext == "" {
		return value, false, nil
	}
	wrapped := encrypted
	changed := false
	var err error
	if encrypted != "" {
		wrapped, changed, err = cipher.RewrapScoped(captchaSecretScope, encrypted)
	} else {
		wrapped, err = cipher.EncryptScoped(captchaSecretScope, plaintext)
		changed = err == nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("rewrap CAPTCHA secret: %w", err)
	}
	if !changed && plaintext == "" {
		return value, false, nil
	}
	config["secret_key_encrypted"], _ = json.Marshal(wrapped)
	delete(config, "secret_key")
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, false, fmt.Errorf("encode rewrapped CAPTCHA setting: %w", err)
	}
	return encoded, true, nil
}
