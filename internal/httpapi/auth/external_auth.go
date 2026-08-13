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

type externalAuthStartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
}

type externalClaims struct {
	Subject       string
	Issuer        string
	Email         string
	EmailVerified bool
	Name          string
}

type oidcClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

var (
	errExternalIdentityConflict = errors.New("This identity is linked to another account")
	errExternalEmailConflict    = errors.New("An account with this email already exists; sign in locally before linking this provider")
	errExternalAccountMissing   = errors.New("No local account matches this identity")
	errExternalVerifiedEmail    = errors.New("A verified email address is required to create an account")
	errExternalAccountInactive  = errors.New("This account is disabled")
)

func registerExternalAuth(group *echo.Group, db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store, cipher settings.SecretCipher, limiter *httpsecurity.RateLimiter) {
	group.GET("/methods", authMethods(settingStore, cipher), limiter.Limit("auth-methods", 60, time.Minute))
	group.GET("/providers/:provider_id/start", externalAuthStart(sessions, settingStore, cipher, false), limiter.Limit("external-auth-start", 30, time.Minute))
	group.POST("/providers/:provider_id/link", externalAuthStart(sessions, settingStore, cipher, true), authn.RequireAuth, limiter.Limit("external-auth-link", 10, time.Minute))
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

func externalAuthStart(sessions *authn.SessionStore, settingStore *settings.Store, cipher settings.SecretCipher, link bool) echo.HandlerFunc {
	return func(c *echo.Context) error {
		configs, _, err := settingStore.AuthProviders(c.Request().Context(), cipher)
		if err != nil {
			return externalAuthStartFailure(c, link, http.StatusServiceUnavailable, "Single sign-on is unavailable")
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
			return externalAuthStartFailure(c, link, http.StatusBadRequest, "The selected sign-in provider is unavailable")
		}
		oauthConfig, _, err := externalOAuthConfig(c.Request().Context(), config)
		if err != nil {
			return externalAuthStartFailure(c, link, http.StatusServiceUnavailable, "Single sign-on is unavailable")
		}
		state, err := randomURLToken(32)
		if err != nil {
			return err
		}
		browserBinding, err := randomURLToken(32)
		if err != nil {
			return err
		}
		verifier := oauth2.GenerateVerifier()
		linkUserID := ""
		if link {
			linkUserID = authn.CurrentUID(c)
		}
		if err = sessions.StoreExternalAuthState(c.Request().Context(), state, authn.ExternalAuthState{
			CodeVerifier: verifier, ReturnPath: validReturnPath(c.QueryParam("return_to")),
			ProviderID: config.ID, LinkUserID: linkUserID,
		}, browserBinding); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "login state storage unavailable")
		}
		sessions.SetExternalAuthBindingCookie(c, state, browserBinding)
		authorizationURL := oauthConfig.AuthCodeURL(
			state, oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(verifier),
		)
		if link {
			return types.JSON(c, http.StatusOK, externalAuthStartResponse{AuthorizationURL: authorizationURL})
		}
		return c.Redirect(http.StatusFound, authorizationURL)
	}
}

func externalAuthCallback(db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store, cipher settings.SecretCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		state := strings.TrimSpace(c.QueryParam("state"))
		browserBinding := sessions.ExternalAuthBinding(c, state)
		sessions.ClearExternalAuthBindingCookie(c, state)
		if state == "" {
			return externalAuthFailure(c, "The single sign-on response is incomplete")
		}
		loginState, err := sessions.ConsumeExternalAuthState(c.Request().Context(), state, browserBinding)
		if err != nil {
			return externalAuthFailure(c, "The single sign-on request expired")
		}
		if loginState.LinkUserID != "" && authn.CurrentUID(c) != loginState.LinkUserID {
			return externalAuthFlowFailure(c, loginState, "Sign in again before linking this provider")
		}
		if c.QueryParam("error") != "" {
			return externalAuthFlowFailure(c, loginState, "The identity provider rejected the login")
		}
		code := strings.TrimSpace(c.QueryParam("code"))
		if code == "" {
			return externalAuthFlowFailure(c, loginState, "The single sign-on response is incomplete")
		}
		config, err := configuredProvider(c.Request().Context(), settingStore, cipher, loginState.ProviderID)
		if err != nil {
			return externalAuthFlowFailure(c, loginState, "The selected sign-in provider is unavailable")
		}
		oauthConfig, oidcProvider, err := externalOAuthConfig(c.Request().Context(), config)
		if err != nil {
			return externalAuthFlowFailure(c, loginState, "Single sign-on is unavailable")
		}
		token, err := oauthConfig.Exchange(c.Request().Context(), code, oauth2.VerifierOption(loginState.CodeVerifier))
		if err != nil {
			return externalAuthFlowFailure(c, loginState, "The identity provider could not complete the login")
		}
		claims, err := externalUserClaims(c.Request().Context(), config, oidcProvider, oauthConfig, token)
		if err != nil {
			return externalAuthFlowFailure(c, loginState, err.Error())
		}

		user, created, err := resolveExternalUser(c.Request().Context(), db, config, claims, loginState.LinkUserID)
		if err != nil {
			if errors.Is(err, errExternalIdentityConflict) || errors.Is(err, errExternalEmailConflict) ||
				errors.Is(err, errExternalAccountMissing) || errors.Is(err, errExternalVerifiedEmail) ||
				errors.Is(err, errExternalAccountInactive) {
				return externalAuthFlowFailure(c, loginState, err.Error())
			}
			return err
		}
		if loginState.LinkUserID != "" {
			audit.SetActor(c, user.Id, user.Email)
			audit.SetResourceID(c, user.Id)
			audit.SetChange(c, nil, map[string]any{
				"user_id": user.Id, "provider_id": config.ID, "provider": config.ProviderName,
				"provider_type": config.Type, "created": created, "linked": true,
			})
			return c.Redirect(http.StatusFound, validReturnPath(loginState.ReturnPath))
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
			"provider_type": config.Type, "created": created, "linked": loginState.LinkUserID != "",
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
		return externalClaimsFromOIDC(idToken.Issuer, claims)
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

func externalClaimsFromOIDC(issuer string, claims oidcClaims) (externalClaims, error) {
	return normalizeExternalClaims(externalClaims{
		Subject: claims.Subject, Issuer: issuer, Email: claims.Email,
		EmailVerified: claims.EmailVerified, Name: claims.Name,
	})
}

func normalizeExternalClaims(claims externalClaims) (externalClaims, error) {
	claims.Subject = strings.TrimSpace(claims.Subject)
	claims.Issuer = strings.TrimSpace(claims.Issuer)
	claims.Email = strings.ToLower(strings.TrimSpace(claims.Email))
	claims.Name = strings.TrimSpace(claims.Name)
	if claims.Subject == "" {
		return externalClaims{}, errors.New("The identity provider subject is missing")
	}
	if claims.Email != "" {
		address, err := mail.ParseAddress(claims.Email)
		if err != nil || address.Address != claims.Email {
			return externalClaims{}, errors.New("The identity provider returned an invalid email address")
		}
	}
	return claims, nil
}

type externalIdentityDecision int

const (
	externalIdentityUseExisting externalIdentityDecision = iota
	externalIdentityBindUser
	externalIdentityCreateUser
)

func decideExternalIdentityUser(identityUserID, linkUserID, emailUserID string, autoCreate, verifiedEmail bool) (externalIdentityDecision, string, error) {
	if identityUserID != "" {
		if linkUserID != "" && identityUserID != linkUserID {
			return 0, "", errExternalIdentityConflict
		}
		return externalIdentityUseExisting, identityUserID, nil
	}
	if linkUserID != "" {
		return externalIdentityBindUser, linkUserID, nil
	}
	if !verifiedEmail {
		return 0, "", errExternalVerifiedEmail
	}
	if emailUserID != "" {
		return 0, "", errExternalEmailConflict
	}
	if !autoCreate {
		return 0, "", errExternalAccountMissing
	}
	return externalIdentityCreateUser, "", nil
}

func resolveExternalUser(ctx context.Context, db *client.Client, config settings.AuthProviderConfig, claims externalClaims, linkUserID string) (*model.User, bool, error) {
	issuer := externalIdentityIssuer(config, claims)
	if issuer == "" {
		return nil, false, errors.New("external identity issuer is missing")
	}

	var resolved *model.User
	created := false
	err := db.Tx(ctx, func(tx *client.Client) error {
		identityLock, err := externalIdentityLockKey(config.ID, issuer, claims.Subject)
		if err != nil {
			return err
		}
		if _, err := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "external-identity:"+identityLock); err != nil {
			return err
		}
		identity, err := tx.ExternalIdentity.Query().Where(query.ExternalIdentity.AND(
			query.ExternalIdentity.ProviderId.Equals(config.ID),
			query.ExternalIdentity.Issuer.Equals(issuer),
			query.ExternalIdentity.Subject.Equals(claims.Subject),
		)).First(ctx)
		if err != nil {
			return err
		}
		identityUserID := ""
		if identity != nil {
			identityUserID = identity.UserId
		}

		var emailUser *model.User
		emailUserID := ""
		verifiedEmail := claims.EmailVerified && claims.Email != ""
		if identity == nil && linkUserID == "" && verifiedEmail {
			if _, err := tx.RawExec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "external-email:"+claims.Email); err != nil {
				return err
			}
			emailUser, err = tx.User.FindUnique(ctx, query.User.Email.Equals(claims.Email))
			if err != nil {
				return err
			}
			if emailUser != nil {
				emailUserID = emailUser.Id
			}
		}
		decision, userID, err := decideExternalIdentityUser(identityUserID, linkUserID, emailUserID, config.AutoCreateUsers, verifiedEmail)
		if err != nil {
			return err
		}

		switch decision {
		case externalIdentityUseExisting:
			resolved, err = tx.User.FindUnique(ctx, query.User.Id.Equals(userID))
			if err != nil {
				return err
			}
			if resolved == nil {
				return errExternalAccountMissing
			}
			if resolved.Status != model.UserStatusACTIVE {
				return errExternalAccountInactive
			}
			if verifiedEmail && identity.Email != claims.Email {
				_, err = tx.ExternalIdentity.Update().Where(query.ExternalIdentity.Id.Equals(identity.Id)).Set(
					query.ExternalIdentity.Email.Set(claims.Email),
				).DoMany(ctx)
			}
			return err
		case externalIdentityBindUser:
			resolved, err = tx.User.FindUnique(ctx, query.User.Id.Equals(userID))
			if err != nil {
				return err
			}
			if resolved == nil {
				return errExternalAccountMissing
			}
		case externalIdentityCreateUser:
			resolved, err = createExternalUser(ctx, tx, claims)
			if err != nil {
				return err
			}
			created = true
		}
		if resolved.Status != model.UserStatusACTIVE {
			return errExternalAccountInactive
		}

		identityEmail := claims.Email
		if identityEmail == "" {
			identityEmail = resolved.Email
		}
		_, err = tx.ExternalIdentity.Create().Set(
			query.ExternalIdentity.UserId.Set(resolved.Id),
			query.ExternalIdentity.ProviderId.Set(config.ID),
			query.ExternalIdentity.Issuer.Set(issuer),
			query.ExternalIdentity.Subject.Set(claims.Subject),
			query.ExternalIdentity.Email.Set(identityEmail),
		).Do(ctx)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return resolved, created, nil
}

func externalIdentityIssuer(config settings.AuthProviderConfig, claims externalClaims) string {
	if config.Type == settings.AuthProviderOAuth2 {
		return "provider:" + config.ID
	}
	return strings.TrimSpace(claims.Issuer)
}

func externalIdentityLockKey(providerID, issuer, subject string) (string, error) {
	encoded, err := json.Marshal([]string{providerID, issuer, subject})
	return string(encoded), err
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

func externalAuthStartFailure(c *echo.Context, link bool, status int, message string) error {
	if link {
		return echo.NewHTTPError(status, message)
	}
	return externalAuthFailure(c, message)
}

func externalAuthFlowFailure(c *echo.Context, state authn.ExternalAuthState, message string) error {
	if state.LinkUserID != "" {
		return c.Redirect(http.StatusFound, "/settings?auth_error="+url.QueryEscape(message))
	}
	return externalAuthFailure(c, message)
}
