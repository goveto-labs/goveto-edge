// Package edgeprotocol contains dependency-free wire types shared by control and agent.
package edgeprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SiteConfig struct {
	SiteID       string              `json:"site_id"`
	Version      uint64              `json:"version"`
	Disabled     bool                `json:"disabled,omitempty"`
	Domains      []string            `json:"domains"`
	Listener     ListenerConfig      `json:"listener"`
	Certificates []CertificateConfig `json:"certificates"`
	Origins      []OriginConfig      `json:"origins"`
	Scheduler    string              `json:"scheduler"`
	Cache        map[string]any      `json:"cache,omitempty"`
	WAF          map[string]any      `json:"waf,omitempty"`
	Access       map[string]any      `json:"access,omitempty"`
	RateLimit    map[string]any      `json:"rate_limit,omitempty"`
}
type ListenerConfig struct {
	HTTPEnabled           bool   `json:"http_enabled"`
	HTTPPort              int    `json:"http_port"`
	RedirectHTTPToHTTPS   bool   `json:"redirect_http_to_https"`
	HTTPSEnabled          bool   `json:"https_enabled"`
	HTTPSPort             int    `json:"https_port"`
	HTTP2Enabled          bool   `json:"http2_enabled"`
	HTTP3Enabled          bool   `json:"http3_enabled"`
	TLSMinVersion         string `json:"tls_min_version"`
	HSTSEnabled           bool   `json:"hsts_enabled"`
	HSTSMaxAge            int    `json:"hsts_max_age"`
	HSTSIncludeSubdomains bool   `json:"hsts_include_subdomains"`
	HSTSPreload           bool   `json:"hsts_preload"`
	OCSPStaplingEnabled   bool   `json:"ocsp_stapling_enabled"`
}
type CertificateConfig struct {
	CertificatePEM string `json:"certificate"`
	PrivateKeyPEM  string `json:"private_key"`
}
type OriginConfig struct {
	Protocol   string `json:"protocol"`
	Address    string `json:"address"`
	HostHeader string `json:"host_header,omitempty"`
	Weight     int    `json:"weight,omitempty"`
}

func (c SiteConfig) Validate() error {
	if c.Disabled {
		if c.SiteID == "" || c.Version == 0 {
			return errors.New("site_id and version are required")
		}
		return nil
	}
	if c.SiteID == "" || c.Version == 0 || len(c.Domains) == 0 || len(c.Origins) == 0 {
		return errors.New("site_id, version, domains and origins are required")
	}
	if !c.Listener.HTTPEnabled && !c.Listener.HTTPSEnabled {
		return errors.New("HTTP or HTTPS must be enabled")
	}
	if c.Listener.HTTPEnabled && (c.Listener.HTTPPort < 1 || c.Listener.HTTPPort > 65535) {
		return errors.New("invalid HTTP port")
	}
	if c.Listener.HTTPSEnabled && (c.Listener.HTTPSPort < 1 || c.Listener.HTTPSPort > 65535) {
		return errors.New("invalid HTTPS port")
	}
	if c.Listener.RedirectHTTPToHTTPS && (!c.Listener.HTTPEnabled || !c.Listener.HTTPSEnabled) {
		return errors.New("redirect requires HTTP and HTTPS")
	}
	if c.Listener.HTTPSEnabled && len(c.Certificates) == 0 {
		return errors.New("HTTPS requires at least one certificate")
	}
	protocol := strings.ToLower(c.Origins[0].Protocol)
	for _, origin := range c.Origins {
		current := strings.ToLower(origin.Protocol)
		if origin.Address == "" || (current != "http" && current != "https") {
			return fmt.Errorf("invalid origin %q", origin.Address)
		}
		if current != protocol {
			return errors.New("mixed HTTP and HTTPS origins are not supported in one standard Caddy transport pool")
		}
	}
	switch c.Scheduler {
	case "", "round_robin", "least_conn", "random", "first", "ip_hash":
	default:
		return fmt.Errorf("unsupported scheduler %q", c.Scheduler)
	}
	return nil
}

type LogRecord struct {
	ID        uint64          `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}
