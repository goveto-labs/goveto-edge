// Package settings reads runtime-modifiable application settings.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
)

const agentGatewayAddressDescription = "Public host and port used by edge nodes to reach the agent gateway"

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

// Initialized reports whether initial instance setup has completed. Instances
// created before the setting existed are treated as initialized when they
// already contain a user, and the setting is backfilled automatically.
func (s *Store) Initialized(ctx context.Context) (bool, error) {
	var initialized bool
	found, err := s.Get(ctx, InstanceInitializedKey, &initialized)
	if err != nil {
		return false, err
	}
	if found && initialized {
		return true, nil
	}

	users, err := s.db.User.Count(ctx)
	if err != nil {
		return false, fmt.Errorf("count users for initialization status: %w", err)
	}
	if users == 0 {
		return false, nil
	}
	if err := s.Set(ctx, InstanceInitializedKey, true, "Whether initial instance setup has completed"); err != nil {
		return false, err
	}
	return true, nil
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
