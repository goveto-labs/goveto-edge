package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v5"
	"golang.org/x/oauth2"

	"goveto-edge/internal/audit"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/password"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type authMethodsResponse struct {
	LocalLoginEnabled bool                         `json:"local_login_enabled"`
	Providers         []authProviderPublicResponse `json:"providers"`
}

type authProviderPublicResponse struct {
	ID           string                    `json:"id"`
	Type         settings.AuthProviderType `json:"type"`
	ProviderName string                    `json:"provider_name"`
	StartURL     string                    `json:"start_url"`
	Enabled      bool                      `json:"enabled"`
}

type externalClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

type oidcClaims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

func registerExternalAuth(group *echo.Group, db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store, cipher settings.SecretCipher, limiter *httpsecurity.RateLimiter) {
	group.GET("/methods", authMethods(settingStore, cipher), limiter.Limit("auth-methods", 60, time.Minute))
	group.GET("/providers/:provider_id/start", externalAuthStart(sessions, settingStore, cipher), limiter.Limit("external-auth-start", 30, time.Minute))
	group.GET("/providers/callback", externalAuthCallback(db, sessions, settingStore, cipher), limiter.Limit("external-auth-callback", 60, time.Minute))
}

func authMethods(settingStore *settings.Store, cipher settings.SecretCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		localEnabled, err := settingStore.LocalLoginEnabled(c.Request().Context())
		if err != nil {
			return err
		}
		configs, _, err := settingStore.AuthProviders(c.Request().Context(), cipher)
		if err != nil {
			return err
		}
		response := authMethodsResponse{
			LocalLoginEnabled: localEnabled,
			Providers:         []authProviderPublicResponse{},
		}
		for _, config := range configs {
			if !config.Enabled {
				continue
			}
			provider := authProviderPublicResponse{
				ID: config.ID, Type: config.Type, ProviderName: config.ProviderName,
				StartURL: "/api/v1/auth/providers/" + url.PathEscape(config.ID) + "/start", Enabled: true,
			}
			response.Providers = append(response.Providers, provider)
		}
		return types.JSON(c, http.StatusOK, response)
	}
}

func externalAuthStart(sessions *authn.SessionStore, settingStore *settings.Store, cipher settings.SecretCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		configs, _, err := settingStore.AuthProviders(c.Request().Context(), cipher)
		if err != nil {
			return externalAuthFailure(c, "Single sign-on is unavailable")
		}
		providerID := strings.TrimSpace(c.Param("provider_id"))
		var config settings.AuthProviderConfig
		for _, candidate := range configs {
			if candidate.Enabled && candidate.ID == providerID {
				config = candidate
				break
			}
		}
		if config.ID == "" {
			return externalAuthFailure(c, "The selected sign-in provider is unavailable")
		}
		oauthConfig, _, err := externalOAuthConfig(c.Request().Context(), config)
		if err != nil {
			return externalAuthFailure(c, "Single sign-on is unavailable")
		}
		state, err := randomURLToken(32)
		if err != nil {
			return err
		}
		verifier := oauth2.GenerateVerifier()
		if err = sessions.StoreExternalAuthState(c.Request().Context(), state, authn.ExternalAuthState{
			CodeVerifier: verifier, ReturnPath: validReturnPath(c.QueryParam("return_to")), ProviderID: config.ID,
		}); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "login state storage unavailable")
		}
		return c.Redirect(http.StatusFound, oauthConfig.AuthCodeURL(
			state, oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(verifier),
		))
	}
}

func externalAuthCallback(db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store, cipher settings.SecretCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if c.QueryParam("error") != "" {
			return externalAuthFailure(c, "The identity provider rejected the login")
		}
		state := strings.TrimSpace(c.QueryParam("state"))
		code := strings.TrimSpace(c.QueryParam("code"))
		if state == "" || code == "" {
			return externalAuthFailure(c, "The single sign-on response is incomplete")
		}
		loginState, err := sessions.ConsumeExternalAuthState(c.Request().Context(), state)
		if err != nil {
			return externalAuthFailure(c, "The single sign-on request expired")
		}
		config, err := configuredProvider(c.Request().Context(), settingStore, cipher, loginState.ProviderID)
		if err != nil {
			return externalAuthFailure(c, "The selected sign-in provider is unavailable")
		}
		oauthConfig, oidcProvider, err := externalOAuthConfig(c.Request().Context(), config)
		if err != nil {
			return externalAuthFailure(c, "Single sign-on is unavailable")
		}
		token, err := oauthConfig.Exchange(c.Request().Context(), code, oauth2.VerifierOption(loginState.CodeVerifier))
		if err != nil {
			return externalAuthFailure(c, "The identity provider could not complete the login")
		}
		claims, err := externalUserClaims(c.Request().Context(), config, oidcProvider, oauthConfig, token)
		if err != nil {
			return externalAuthFailure(c, err.Error())
		}

		user, err := db.User.FindUnique(c.Request().Context(), query.User.Email.Equals(claims.Email))
		if err != nil {
			return err
		}
		created := false
		if user == nil {
			if !config.AutoCreateUsers {
				return externalAuthFailure(c, "No local account matches this identity")
			}
			user, err = createExternalUser(c.Request().Context(), db, claims)
			if err != nil {
				return err
			}
			created = true
		}
		if user.Status != model.UserStatusACTIVE {
			return externalAuthFailure(c, "This account is disabled")
		}
		now := time.Now().UTC()
		if _, err = db.User.Update().Where(query.User.Id.Equals(user.Id)).Set(
			query.User.FailedLoginAttempts.Set(0), query.User.LastFailedLoginAt.SetNull(),
			query.User.LockedUntil.SetNull(), query.User.LastLoginAt.Set(now),
		).DoMany(c.Request().Context()); err != nil {
			return err
		}
		sessionToken, err := sessions.Create(c.Request().Context(), user.Id, authn.SessionMetadata{
			IPAddress: c.RealIP(), UserAgent: c.Request().UserAgent(),
		})
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "session storage unavailable")
		}
		if err = sessions.SetCookie(c, sessionToken); err != nil {
			return err
		}
		audit.SetActor(c, user.Id, user.Email)
		audit.SetResourceID(c, user.Id)
		audit.SetChange(c, nil, map[string]any{
			"user_id": user.Id, "provider_id": config.ID, "provider": config.ProviderName,
			"provider_type": config.Type, "created": created,
		})
		return c.Redirect(http.StatusFound, validReturnPath(loginState.ReturnPath))
	}
}

func configuredProvider(ctx context.Context, settingStore *settings.Store, cipher settings.SecretCipher, id string) (settings.AuthProviderConfig, error) {
	configs, _, err := settingStore.AuthProviders(ctx, cipher)
	if err != nil {
		return settings.AuthProviderConfig{}, err
	}
	for _, config := range configs {
		if config.Enabled && config.ID == id {
			return config, nil
		}
	}
	return settings.AuthProviderConfig{}, errors.New("authentication provider is disabled or missing")
}

func externalOAuthConfig(ctx context.Context, config settings.AuthProviderConfig) (oauth2.Config, *oidc.Provider, error) {
	var endpoint oauth2.Endpoint
	var provider *oidc.Provider
	if config.Type == settings.AuthProviderOIDC {
		providerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		provider, err = oidc.NewProvider(providerCtx, config.IssuerURL)
		if err != nil {
			return oauth2.Config{}, nil, err
		}
		endpoint = provider.Endpoint()
	} else {
		endpoint = oauth2.Endpoint{AuthURL: config.AuthorizationURL, TokenURL: config.TokenURL, AuthStyle: oauth2.AuthStyleAutoDetect}
	}
	return oauth2.Config{
		ClientID: config.ClientID, ClientSecret: config.ClientSecret, RedirectURL: config.RedirectURL,
		Endpoint: endpoint, Scopes: config.Scopes,
	}, provider, nil
}

func externalUserClaims(ctx context.Context, config settings.AuthProviderConfig, provider *oidc.Provider, oauthConfig oauth2.Config, token *oauth2.Token) (externalClaims, error) {
	if config.Type == settings.AuthProviderOIDC {
		rawIDToken, ok := token.Extra("id_token").(string)
		if !ok || rawIDToken == "" {
			return externalClaims{}, errors.New("The identity provider did not return an ID token")
		}
		idToken, err := provider.Verifier(&oidc.Config{ClientID: config.ClientID}).Verify(ctx, rawIDToken)
		if err != nil {
			return externalClaims{}, errors.New("The identity token could not be verified")
		}
		var claims oidcClaims
		if err = idToken.Claims(&claims); err != nil {
			return externalClaims{}, errors.New("The identity token claims are invalid")
		}
		email := claims.Email
		verified := claims.EmailVerified
		if config.Preset == "MICROSOFT_ENTRA" {
			if strings.TrimSpace(email) == "" {
				email = claims.PreferredUsername
			}
			verified = strings.TrimSpace(email) != ""
		}
		return normalizeExternalClaims(externalClaims{
			Subject: claims.Subject, Email: email, EmailVerified: verified, Name: claims.Name,
		})
	}

	client := oauthConfig.Client(ctx, token)
	var profile map[string]any
	if err := getProviderJSON(ctx, client, config.UserInfoURL, &profile); err != nil {
		return externalClaims{}, errors.New("The identity provider user profile is unavailable")
	}
	claims := externalClaims{
		Subject:       firstStringClaim(profile, "sub", "id"),
		Email:         firstStringClaim(profile, "email"),
		Name:          firstStringClaim(profile, "name", "login"),
		EmailVerified: boolClaim(profile, "email_verified"),
	}
	if config.EmailURL != "" {
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := getProviderJSON(ctx, client, config.EmailURL, &emails); err != nil {
			return externalClaims{}, errors.New("The identity provider email address is unavailable")
		}
		for _, email := range emails {
			if email.Verified && (email.Primary || claims.Email == "") {
				claims.Email = email.Email
				claims.EmailVerified = true
				if email.Primary {
					break
				}
			}
		}
	}
	return normalizeExternalClaims(claims)
}

func normalizeExternalClaims(claims externalClaims) (externalClaims, error) {
	claims.Subject = strings.TrimSpace(claims.Subject)
	claims.Email = strings.ToLower(strings.TrimSpace(claims.Email))
	claims.Name = strings.TrimSpace(claims.Name)
	if claims.Subject == "" || claims.Email == "" || !claims.EmailVerified {
		return externalClaims{}, errors.New("A verified email address is required")
	}
	address, err := mail.ParseAddress(claims.Email)
	if err != nil || address.Address != claims.Email {
		return externalClaims{}, errors.New("The identity provider returned an invalid email address")
	}
	return claims, nil
}

func getProviderJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("provider returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func firstStringClaim(claims map[string]any, names ...string) string {
	for _, name := range names {
		switch value := claims[name].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return value
			}
		case json.Number:
			return value.String()
		case float64:
			return fmt.Sprintf("%.0f", value)
		}
	}
	return ""
}

func boolClaim(claims map[string]any, name string) bool {
	value, _ := claims[name].(bool)
	return value
}

func createExternalUser(ctx context.Context, db *client.Client, claims externalClaims) (*model.User, error) {
	randomPassword, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}
	hash, err := password.Hash(randomPassword)
	if err != nil {
		return nil, err
	}
	name := claims.Name
	if name == "" {
		name = strings.Split(claims.Email, "@")[0]
	}
	return db.User.Create().Set(
		query.User.Email.Set(claims.Email), query.User.PasswordHash.Set(hash),
		query.User.Name.Set(name), query.User.Role.Set(model.UserRoleVIEWER),
		query.User.Status.Set(model.UserStatusACTIVE),
	).Do(ctx)
}

func randomURLToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validReturnPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, `\`) {
		return "/"
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "/"
	}
	return value
}

func externalAuthFailure(c *echo.Context, message string) error {
	return c.Redirect(http.StatusFound, "/login?auth_error="+url.QueryEscape(message))
}
