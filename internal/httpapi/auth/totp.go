package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/pquerna/otp/totp"

	"goveto-edge/internal/audit"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/password"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const recoveryCodeCount = 10

type totpSetupResponse struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

type totpMutationRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
	Secret   string `json:"secret,omitempty"`
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

type securityPolicyResponse struct {
	RequireTOTP bool `json:"require_totp"`
}

func registerTOTP(group *echo.Group, db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store, sensitive echo.MiddlewareFunc) {
	group.POST("/totp/setup", setupTOTP(db), authn.RequireAuth)
	group.POST("/totp/enable", enableTOTP(db, sessions), authn.RequireAuth, sensitive)
	group.POST("/totp/reset", resetTOTP(db, sessions), authn.RequireAuth, sensitive)
	group.POST("/totp/recovery-codes", regenerateRecoveryCodes(db, sessions), authn.RequireAuth, sensitive)
	group.DELETE("/totp", disableTOTP(db, sessions, settingStore), authn.RequireAuth, sensitive)
	group.GET("/security-policy", getSecurityPolicy(settingStore), authn.RequireAuth)
	group.PUT("/security-policy", updateSecurityPolicy(settingStore), authn.RequireAuth)
}

func setupTOTP(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		user, err := currentUser(c, db)
		if err != nil {
			return err
		}
		if hasTOTP(user) {
			return echo.NewHTTPError(http.StatusConflict, "TOTP is already enabled")
		}
		return writeTOTPSetup(c, user.Email)
	}
}

func enableTOTP(db *client.Client, sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input totpMutationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		user, err := verifyCurrentPassword(c, db, input.Password)
		if err != nil {
			return err
		}
		// Replacing an existing TOTP configuration must go through /totp/reset,
		// which additionally verifies the current second factor.
		if hasTOTP(user) {
			return echo.NewHTTPError(http.StatusConflict, "TOTP is already enabled")
		}
		secret := strings.TrimSpace(input.Secret)
		if secret == "" || !totp.Validate(strings.TrimSpace(input.Code), secret) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid TOTP secret or code")
		}
		firstUse, err := sessions.ConsumeTOTPCode(c.Request().Context(), user.Id, input.Code)
		if err != nil {
			return err
		}
		if !firstUse {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid TOTP secret or code")
		}
		codes, hashes, err := newRecoveryCodes()
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(hashes)
		_, err = db.User.Update().Where(query.User.Id.Equals(user.Id)).Set(
			query.User.TotpSecret.Set(secret), query.User.TotpRecoveryCodes.Set(encoded),
		).DoMany(c.Request().Context())
		if err != nil {
			return err
		}
		if err = sessions.RevokeAll(c.Request().Context(), user.Id, authn.CurrentSessionID(c)); err != nil {
			return err
		}
		audit.SetChange(c, map[string]any{"totp_enabled": false}, map[string]any{"totp_enabled": true})
		return types.JSON(c, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
	}
}

func resetTOTP(db *client.Client, sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input totpMutationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		user, err := verifyCurrentPassword(c, db, input.Password)
		if err != nil {
			return err
		}
		if !hasTOTP(user) {
			return echo.NewHTTPError(http.StatusConflict, "TOTP is not enabled")
		}
		valid, err := verifySecondFactor(c.Request().Context(), db, sessions, user, input.Code)
		if err != nil {
			return err
		}
		if !valid {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid TOTP or recovery code")
		}
		if _, err = db.User.Update().Where(query.User.Id.Equals(user.Id)).Set(
			query.User.TotpSecret.SetNull(), query.User.TotpRecoveryCodes.SetNull(),
		).DoMany(c.Request().Context()); err != nil {
			return err
		}
		if err = sessions.RevokeAll(c.Request().Context(), user.Id, authn.CurrentSessionID(c)); err != nil {
			return err
		}
		audit.SetChange(c, map[string]any{"totp_enabled": true}, map[string]any{"totp_enabled": false})
		return writeTOTPSetup(c, user.Email)
	}
}

func regenerateRecoveryCodes(db *client.Client, sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input totpMutationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		user, err := verifyCurrentPassword(c, db, input.Password)
		if err != nil {
			return err
		}
		if !hasTOTP(user) {
			return echo.NewHTTPError(http.StatusConflict, "TOTP is not enabled")
		}
		valid, err := verifySecondFactor(c.Request().Context(), db, sessions, user, input.Code)
		if err != nil {
			return err
		}
		if !valid {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid TOTP or recovery code")
		}
		codes, hashes, err := newRecoveryCodes()
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(hashes)
		if _, err = db.User.Update().Where(query.User.Id.Equals(user.Id)).Set(
			query.User.TotpRecoveryCodes.Set(encoded),
		).DoMany(c.Request().Context()); err != nil {
			return err
		}
		if err = sessions.RevokeAll(c.Request().Context(), user.Id, authn.CurrentSessionID(c)); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
	}
}

func disableTOTP(db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		required, err := settingStore.RequireTOTP(c.Request().Context())
		if err != nil {
			return err
		}
		if required {
			return echo.NewHTTPError(http.StatusConflict, "TOTP is required by security policy")
		}
		var input totpMutationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		user, err := verifyCurrentPassword(c, db, input.Password)
		if err != nil {
			return err
		}
		if !hasTOTP(user) {
			return echo.NewHTTPError(http.StatusConflict, "TOTP is not enabled")
		}
		valid, err := verifySecondFactor(c.Request().Context(), db, sessions, user, input.Code)
		if err != nil {
			return err
		}
		if !valid {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid TOTP or recovery code")
		}
		_, err = db.User.Update().Where(query.User.Id.Equals(user.Id)).Set(
			query.User.TotpSecret.SetNull(), query.User.TotpRecoveryCodes.SetNull(),
		).DoMany(c.Request().Context())
		if err != nil {
			return err
		}
		if err = sessions.RevokeAll(c.Request().Context(), user.Id, authn.CurrentSessionID(c)); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func getSecurityPolicy(settingStore *settings.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		required, err := settingStore.RequireTOTP(c.Request().Context())
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, securityPolicyResponse{RequireTOTP: required})
	}
}

func updateSecurityPolicy(settingStore *settings.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		user, ok := authn.CurrentUser(c.Request().Context(), authn.CurrentUID(c))
		if !ok || user.Role != model.UserRoleADMIN {
			return echo.NewHTTPError(http.StatusForbidden, "administrator role required")
		}
		var input securityPolicyResponse
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if err := settingStore.SetRequireTOTP(c.Request().Context(), input.RequireTOTP); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, input)
	}
}

func RequireTOTPEnrollment(settingStore *settings.Store) echo.MiddlewareFunc {
	allowed := map[string]bool{
		"/api/v1/auth/login": true, "/api/v1/auth/register": true,
		"/api/v1/auth/registration-config": true, "/api/v1/auth/password-reset": true,
		"/api/v1/init": true, "/api/v1/init/status": true,
		"/api/v1/auth/me": true, "/api/v1/auth/logout": true,
		"/api/v1/auth/totp/setup": true, "/api/v1/auth/totp/enable": true,
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			uid := authn.CurrentUID(c)
			if uid == "" || !strings.HasPrefix(c.Request().URL.Path, "/api/v1/") || allowed[c.Request().URL.Path] ||
				strings.HasPrefix(c.Request().URL.Path, "/api/v1/health") {
				return next(c)
			}
			required, err := settingStore.RequireTOTP(c.Request().Context())
			if err != nil {
				return err
			}
			if user, ok := authn.CurrentUser(c.Request().Context(), uid); required && ok && !hasTOTP(user) {
				return echo.NewHTTPError(http.StatusForbidden, "TOTP enrollment is required")
			}
			return next(c)
		}
	}
}

func writeTOTPSetup(c *echo.Context, email string) error {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Goveto Edge", AccountName: email})
	if err != nil {
		return err
	}
	return types.JSON(c, http.StatusOK, totpSetupResponse{Secret: key.Secret(), URI: key.URL()})
}

func verifyCurrentPassword(c *echo.Context, db *client.Client, value string) (*model.User, error) {
	user, err := currentUser(c, db)
	if err != nil {
		return nil, err
	}
	if value == "" || !password.Verify(user.PasswordHash, value) {
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "current password is invalid")
	}
	return user, nil
}

func currentUser(c *echo.Context, db *client.Client) (*model.User, error) {
	user, err := db.User.FindUnique(c.Request().Context(), query.User.Id.Equals(authn.CurrentUID(c)))
	if err != nil {
		return nil, err
	}
	if user == nil || user.Status != model.UserStatusACTIVE {
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "user is unavailable")
	}
	return user, nil
}

func verifySecondFactor(ctx context.Context, db *client.Client, sessions *authn.SessionStore, user *model.User, value string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" || !hasTOTP(user) {
		return false, nil
	}
	if totp.Validate(value, strings.TrimSpace(*user.TotpSecret)) {
		// A code only counts once: replaying a captured code within its
		// validity window must fail even though totp.Validate accepts it.
		return sessions.ConsumeTOTPCode(ctx, user.Id, value)
	}
	return consumeRecoveryCode(ctx, db, user.Id, value)
}

func consumeRecoveryCode(ctx context.Context, db *client.Client, userID, value string) (bool, error) {
	wanted := hashRecoveryCode(value)
	consumed := false
	err := db.Tx(ctx, func(tx *client.Client) error {
		type recoveryRow struct {
			Codes *json.RawMessage `db:"totp_recovery_codes"`
		}
		rows, err := client.Raw[recoveryRow](ctx, tx,
			"SELECT totp_recovery_codes FROM users WHERE id=$1 FOR UPDATE", userID)
		if err != nil || len(rows) != 1 || rows[0].Codes == nil {
			return err
		}
		var hashes []string
		if err := json.Unmarshal(*rows[0].Codes, &hashes); err != nil {
			return err
		}
		for index, hash := range hashes {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(wanted)) == 1 {
				hashes = append(hashes[:index], hashes[index+1:]...)
				encoded, _ := json.Marshal(hashes)
				_, err = tx.User.Update().Where(query.User.Id.Equals(userID)).Set(
					query.User.TotpRecoveryCodes.Set(encoded),
				).DoMany(ctx)
				consumed = err == nil
				return err
			}
		}
		return nil
	})
	return consumed, err
}

func newRecoveryCodes() ([]string, []string, error) {
	codes := make([]string, recoveryCodeCount)
	hashes := make([]string, recoveryCodeCount)
	for index := range codes {
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		encoded := strings.ToUpper(hex.EncodeToString(raw))
		codes[index] = encoded[:8] + "-" + encoded[8:]
		hashes[index] = hashRecoveryCode(codes[index])
	}
	return codes, hashes, nil
}

func hashRecoveryCode(value string) string {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:])
}
