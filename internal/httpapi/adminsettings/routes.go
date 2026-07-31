// Package adminsettings exposes instance-level settings to the instance owner.
package adminsettings

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/settings"
)

type response struct {
	AgentGatewayPublicAddress string                   `json:"agent_gateway_public_address"`
	HTTPProxy                 settings.HTTPProxyConfig `json:"http_proxy"`
	Authentication            authenticationResponse   `json:"authentication"`
	RestartRequired           bool                     `json:"restart_required"`
	Restarting                bool                     `json:"restarting"`
}

type authenticationResponse struct {
	LocalLoginEnabled bool                             `json:"local_login_enabled"`
	RequireTOTP       bool                             `json:"require_totp"`
	Providers         []authenticationProviderResponse `json:"providers"`
}

type authenticationProviderResponse struct {
	ID                     string                    `json:"id"`
	Type                   settings.AuthProviderType `json:"type"`
	Preset                 string                    `json:"preset"`
	Enabled                bool                      `json:"enabled"`
	ProviderName           string                    `json:"provider_name"`
	IssuerURL              string                    `json:"issuer_url"`
	AuthorizationURL       string                    `json:"authorization_url"`
	TokenURL               string                    `json:"token_url"`
	UserInfoURL            string                    `json:"user_info_url"`
	EmailURL               string                    `json:"email_url"`
	ClientID               string                    `json:"client_id"`
	ClientSecretConfigured bool                      `json:"client_secret_configured"`
	RedirectURL            string                    `json:"redirect_url"`
	Scopes                 []string                  `json:"scopes"`
	AutoCreateUsers        bool                      `json:"auto_create_users"`
}

type updateRequest struct {
	AgentGatewayPublicAddress string                   `json:"agent_gateway_public_address"`
	HTTPProxy                 settings.HTTPProxyConfig `json:"http_proxy"`
	Authentication            authenticationRequest    `json:"authentication"`
	Restart                   bool                     `json:"restart"`
}

type authenticationRequest struct {
	LocalLoginEnabled bool                            `json:"local_login_enabled"`
	RequireTOTP       bool                            `json:"require_totp"`
	Providers         []authenticationProviderRequest `json:"providers"`
}

type authenticationProviderRequest struct {
	ID               string                    `json:"id"`
	Type             settings.AuthProviderType `json:"type"`
	Preset           string                    `json:"preset"`
	Enabled          bool                      `json:"enabled"`
	ProviderName     string                    `json:"provider_name"`
	IssuerURL        string                    `json:"issuer_url"`
	AuthorizationURL string                    `json:"authorization_url"`
	TokenURL         string                    `json:"token_url"`
	UserInfoURL      string                    `json:"user_info_url"`
	EmailURL         string                    `json:"email_url"`
	ClientID         string                    `json:"client_id"`
	ClientSecret     string                    `json:"client_secret"`
	RedirectURL      string                    `json:"redirect_url"`
	Scopes           []string                  `json:"scopes"`
	AutoCreateUsers  bool                      `json:"auto_create_users"`
}

func Register(e *echo.Echo, settingStore *settings.Store, cipher settings.SecretCipher, restartControlPlane func()) {
	group := e.Group(
		"/api/v1/admin/settings",
		authn.RequireAuth,
		requireInstanceOwner(settingStore),
	)
	group.GET("", get(settingStore, cipher))
	group.PUT("", update(settingStore, cipher, restartControlPlane))
}

func requireInstanceOwner(settingStore *settings.Store) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			allowed, err := settingStore.IsInstanceOwner(c.Request().Context(), authn.CurrentUID(c))
			if err != nil {
				return err
			}
			if !allowed {
				return echo.NewHTTPError(http.StatusForbidden, "instance owner access required")
			}
			return next(c)
		}
	}
}

// @summary Get instance settings
// @description Return system-level settings visible only to the instance owner.
// @Tags admin settings
func get(settingStore *settings.Store, cipher settings.SecretCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		result, err := readResponse(c, settingStore, cipher, false, false)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Update instance settings
// @description Update system-level settings as the instance owner.
// @Tags admin settings
func update(settingStore *settings.Store, cipher settings.SecretCipher, restartControlPlane func()) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input updateRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		address, err := settings.ValidateAgentGatewayPublicAddress(input.AgentGatewayPublicAddress)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err = input.HTTPProxy.NormalizeAndValidate(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		currentAddress, _, err := settingStore.AgentGatewayPublicAddress(c.Request().Context())
		if err != nil {
			return err
		}
		currentProxy, _, err := settingStore.HTTPProxy(c.Request().Context())
		if err != nil {
			return err
		}
		currentProviders, _, err := settingStore.AuthProviders(c.Request().Context(), cipher)
		if err != nil {
			return err
		}
		currentLocalLogin, err := settingStore.LocalLoginEnabled(c.Request().Context())
		if err != nil {
			return err
		}
		runtimeProviders, storedProviders, err := buildProviderConfigs(input.Authentication.Providers, currentProviders)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		enabledProviders := 0
		for _, provider := range runtimeProviders {
			if provider.Enabled {
				enabledProviders++
				if input.Authentication.RequireTOTP && provider.AutoCreateUsers {
					return echo.NewHTTPError(http.StatusBadRequest, "automatic external user creation cannot be combined with required TOTP")
				}
			}
		}
		if !input.Authentication.LocalLoginEnabled && enabledProviders == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "local login or an external provider must remain enabled")
		}
		if !input.Authentication.LocalLoginEnabled && !authProvidersRuntimeEqual(currentProviders, runtimeProviders) {
			return echo.NewHTTPError(http.StatusBadRequest, "save and verify provider changes while local login remains enabled before disabling local login")
		}

		restartRequired := currentAddress != address || !reflect.DeepEqual(currentProxy, input.HTTPProxy)
		if currentAddress != address {
			if err = settingStore.SetAgentGatewayPublicAddress(c.Request().Context(), address); err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(currentProxy, input.HTTPProxy) {
			if err = settingStore.SetHTTPProxy(c.Request().Context(), input.HTTPProxy); err != nil {
				return err
			}
		}
		if currentLocalLogin != input.Authentication.LocalLoginEnabled {
			if err = settingStore.SetLocalLoginEnabled(c.Request().Context(), input.Authentication.LocalLoginEnabled); err != nil {
				return err
			}
		}
		if err = settingStore.SetRequireTOTP(c.Request().Context(), input.Authentication.RequireTOTP); err != nil {
			return err
		}
		if err = settingStore.SetAuthProviders(c.Request().Context(), storedProviders, cipher); err != nil {
			return err
		}

		restarting := restartRequired && input.Restart && restartControlPlane != nil
		result, err := readResponse(c, settingStore, cipher, restartRequired, restarting)
		if err != nil {
			return err
		}
		if restarting {
			go func() {
				time.Sleep(500 * time.Millisecond)
				restartControlPlane()
			}()
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

func buildProviderConfigs(inputs []authenticationProviderRequest, current []settings.AuthProviderConfig) ([]settings.AuthProviderConfig, []settings.AuthProviderConfig, error) {
	if len(inputs) > 20 {
		return nil, nil, errors.New("at most 20 authentication providers may be configured")
	}
	currentByID := make(map[string]settings.AuthProviderConfig, len(current))
	for _, provider := range current {
		currentByID[provider.ID] = provider
	}
	runtimeProviders := make([]settings.AuthProviderConfig, 0, len(inputs))
	storedProviders := make([]settings.AuthProviderConfig, 0, len(inputs))
	seenIDs := make(map[string]bool, len(inputs))
	seenNames := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if input.ID == "" {
			input.ID = uuid.NewString()
		}
		provider := settings.AuthProviderConfig{
			ID: input.ID, Type: input.Type, Preset: input.Preset, Enabled: input.Enabled,
			ProviderName: input.ProviderName, IssuerURL: input.IssuerURL,
			AuthorizationURL: input.AuthorizationURL, TokenURL: input.TokenURL,
			UserInfoURL: input.UserInfoURL, EmailURL: input.EmailURL,
			ClientID: input.ClientID, ClientSecret: input.ClientSecret,
			RedirectURL: input.RedirectURL, Scopes: input.Scopes,
			AutoCreateUsers: input.AutoCreateUsers,
		}
		if provider.ClientSecret == "" {
			provider.ClientSecret = currentByID[provider.ID].ClientSecret
		}
		if err := provider.NormalizeAndValidate(); err != nil {
			return nil, nil, fmt.Errorf("authentication provider %q: %w", provider.ProviderName, err)
		}
		nameKey := strings.ToLower(provider.ProviderName)
		if seenIDs[provider.ID] || seenNames[nameKey] {
			return nil, nil, fmt.Errorf("authentication provider IDs and names must be unique")
		}
		seenIDs[provider.ID] = true
		seenNames[nameKey] = true
		runtimeProviders = append(runtimeProviders, provider)
		if input.ClientSecret == "" {
			provider.ClientSecret = ""
		}
		storedProviders = append(storedProviders, provider)
	}
	return runtimeProviders, storedProviders, nil
}

func authProvidersRuntimeEqual(left, right []settings.AuthProviderConfig) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !authProviderRuntimeEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func authProviderRuntimeEqual(left, right settings.AuthProviderConfig) bool {
	return left.ID == right.ID && left.Type == right.Type && left.Preset == right.Preset &&
		left.Enabled == right.Enabled && left.ProviderName == right.ProviderName &&
		left.IssuerURL == right.IssuerURL && left.AuthorizationURL == right.AuthorizationURL &&
		left.TokenURL == right.TokenURL && left.UserInfoURL == right.UserInfoURL &&
		left.EmailURL == right.EmailURL && left.ClientID == right.ClientID &&
		left.ClientSecret == right.ClientSecret && left.RedirectURL == right.RedirectURL &&
		left.AutoCreateUsers == right.AutoCreateUsers && slices.Equal(left.Scopes, right.Scopes)
}

func readResponse(c *echo.Context, settingStore *settings.Store, cipher settings.SecretCipher, restartRequired, restarting bool) (response, error) {
	address, found, err := settingStore.AgentGatewayPublicAddress(c.Request().Context())
	if err != nil {
		return response{}, err
	}
	if !found {
		return response{}, echo.NewHTTPError(http.StatusInternalServerError, "agent gateway public address is not configured")
	}
	proxy, _, err := settingStore.HTTPProxy(c.Request().Context())
	if err != nil {
		return response{}, err
	}
	localEnabled, err := settingStore.LocalLoginEnabled(c.Request().Context())
	if err != nil {
		return response{}, err
	}
	requireTOTP, err := settingStore.RequireTOTP(c.Request().Context())
	if err != nil {
		return response{}, err
	}
	providers, _, err := settingStore.AuthProviders(c.Request().Context(), cipher)
	if err != nil {
		return response{}, err
	}
	providerResponses := make([]authenticationProviderResponse, 0, len(providers))
	for _, provider := range providers {
		providerResponses = append(providerResponses, authenticationProviderResponse{
			ID: provider.ID, Type: provider.Type, Preset: provider.Preset,
			Enabled: provider.Enabled, ProviderName: provider.ProviderName,
			IssuerURL: provider.IssuerURL, AuthorizationURL: provider.AuthorizationURL,
			TokenURL: provider.TokenURL, UserInfoURL: provider.UserInfoURL, EmailURL: provider.EmailURL,
			ClientID: provider.ClientID, ClientSecretConfigured: provider.SecretConfigured,
			RedirectURL: provider.RedirectURL, Scopes: provider.Scopes,
			AutoCreateUsers: provider.AutoCreateUsers,
		})
	}
	return response{
		AgentGatewayPublicAddress: address,
		HTTPProxy:                 proxy,
		Authentication: authenticationResponse{
			LocalLoginEnabled: localEnabled,
			RequireTOTP:       requireTOTP,
			Providers:         providerResponses,
		},
		RestartRequired: restartRequired,
		Restarting:      restarting,
	}, nil
}
