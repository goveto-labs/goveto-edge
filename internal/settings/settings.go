// Package settings reads runtime-modifiable application settings.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

const (
	RegistrationEnabledKey = "auth.registration.enabled"
	CaptchaKey             = "auth.captcha"
	InstanceInitializedKey = "instance.initialized"
)

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
