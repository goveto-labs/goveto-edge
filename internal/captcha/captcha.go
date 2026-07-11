// Package captcha verifies registration CAPTCHA challenges.
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ProviderRecaptcha  = "recaptcha"
	ProviderCloudflare = "cloudflare"
)

type Verifier struct {
	client *http.Client
}

func New() *Verifier {
	return &Verifier{client: &http.Client{Timeout: 5 * time.Second}}
}

func (v *Verifier) Verify(ctx context.Context, provider, secret, token, remoteIP string) (bool, error) {
	endpoint := ""
	switch strings.ToLower(provider) {
	case ProviderRecaptcha:
		endpoint = "https://www.google.com/recaptcha/api/siteverify"
	case ProviderCloudflare, "turnstile":
		endpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	default:
		return false, fmt.Errorf("unsupported captcha provider %q", provider)
	}
	if secret == "" || token == "" {
		return false, errors.New("captcha secret and token are required")
	}
	form := url.Values{"secret": {secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := v.client.Do(req)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("captcha provider returned %s", response.Status)
	}
	var result struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.Success, nil
}
