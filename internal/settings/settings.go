// Package settings reads runtime-modifiable application settings.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

const (
	RegistrationEnabledKey = "auth.registration.enabled"
	CaptchaKey             = "auth.captcha"
)

type Store struct {
	db *client.Client
}

func New(db *client.Client) *Store {
	return &Store{db: db}
}

func (s *Store) Get(ctx context.Context, key string, target any) (bool, error) {
	setting, err := s.db.DynamicSetting.FindUnique(ctx, query.DynamicSetting.Key.Equals(key))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read setting %q: %w", key, err)
	}
	if err := json.Unmarshal(setting.ValueJson, target); err != nil {
		return false, fmt.Errorf("decode setting %q: %w", key, err)
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
