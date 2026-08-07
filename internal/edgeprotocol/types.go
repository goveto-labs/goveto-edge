// Package edgeprotocol contains wire types shared by the control plane and agent.
package edgeprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

type NodeHardwareProfile struct {
	Architecture                 string    `json:"architecture"`
	CPUModel                     string    `json:"cpu_model"`
	CacheDiskWriteBytesPerSecond *uint64   `json:"cache_disk_write_bytes_per_second,omitempty"`
	BenchmarkBytes               *uint64   `json:"benchmark_bytes,omitempty"`
	BenchmarkDurationMS          *int64    `json:"benchmark_duration_ms,omitempty"`
	MeasuredAt                   time.Time `json:"measured_at"`
	DiskBenchmarkError           string    `json:"disk_benchmark_error,omitempty"`
}

type SiteConfig struct {
	SiteID         string                `json:"site_id"`
	Version        uint64                `json:"version"`
	Disabled       bool                  `json:"disabled,omitempty"`
	Domains        []string              `json:"domains"`
	Listener       ListenerConfig        `json:"listener"`
	Certificates   []CertificateConfig   `json:"certificates"`
	ACMEChallenges []ACMEChallengeConfig `json:"acme_challenges,omitempty"`
	Origins        []OriginConfig        `json:"origins"`
	Scheduler      string                `json:"scheduler"`
	OriginPolicy   OriginPolicyConfig    `json:"origin_policy"`
	Cache          map[string]any        `json:"cache,omitempty"`
	Compression    map[string]any        `json:"compression,omitempty"`
	Delivery       map[string]any        `json:"delivery,omitempty"`
	WAF            map[string]any        `json:"waf,omitempty"`
	Access         map[string]any        `json:"access,omitempty"`
	RateLimit      map[string]any        `json:"rate_limit,omitempty"`
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
type ACMEChallengeConfig struct {
	Domain  string `json:"domain"`
	Token   string `json:"token"`
	KeyAuth string `json:"key_auth"`
}
type OriginConfig struct {
	Protocol   string `json:"protocol"`
	Address    string `json:"address"`
	HostHeader string `json:"host_header,omitempty"`
	Weight     int    `json:"weight,omitempty"`
	Priority   int    `json:"priority,omitempty"`
}

type OriginPolicyConfig struct {
	TimeoutMS     int                       `json:"timeout_ms,omitempty"`
	Headers       map[string][]string       `json:"headers,omitempty"`
	PassiveHealth OriginPassiveHealthConfig `json:"passive_health"`
	Transport     OriginTransportConfig     `json:"transport"`
	Retry         OriginRetryConfig         `json:"retry"`
}

type OriginPassiveHealthConfig struct {
	Enabled               bool  `json:"enabled"`
	FailDurationMS        int   `json:"fail_duration_ms,omitempty"`
	MaxFails              int   `json:"max_fails,omitempty"`
	UnhealthyStatus       []int `json:"unhealthy_status,omitempty"`
	UnhealthyLatencyMS    int   `json:"unhealthy_latency_ms,omitempty"`
	UnhealthyRequestCount int   `json:"unhealthy_request_count,omitempty"`
}

type OriginTransportConfig struct {
	DialTimeoutMS                int      `json:"dial_timeout_ms,omitempty"`
	TLSHandshakeTimeoutMS        int      `json:"tls_handshake_timeout_ms,omitempty"`
	ResponseHeaderTimeoutMS      int      `json:"response_header_timeout_ms,omitempty"`
	ReadTimeoutMS                int      `json:"read_timeout_ms,omitempty"`
	WriteTimeoutMS               int      `json:"write_timeout_ms,omitempty"`
	MaxConnsPerHost              int      `json:"max_conns_per_host,omitempty"`
	KeepAliveMaxIdleConnsPerHost int      `json:"keep_alive_max_idle_conns_per_host,omitempty"`
	KeepAliveIdleTimeoutMS       int      `json:"keep_alive_idle_timeout_ms,omitempty"`
	IPVersion                    string   `json:"ip_version,omitempty"`
	TLSServerName                string   `json:"tls_server_name,omitempty"`
	TLSRootCAPEM                 []string `json:"tls_root_ca_pem,omitempty"`
	TLSClientCertificatePEM      string   `json:"tls_client_certificate_pem,omitempty"`
	TLSClientPrivateKeyPEM       string   `json:"tls_client_private_key_pem,omitempty"`
	MTLSConfigured               bool     `json:"mtls_configured,omitempty"`
	TLSInsecureSkipVerify        bool     `json:"tls_insecure_skip_verify,omitempty"`
}

type OriginRetryConfig struct {
	Retries       int `json:"retries,omitempty"`
	TryDurationMS int `json:"try_duration_ms,omitempty"`
	TryIntervalMS int `json:"try_interval_ms,omitempty"`
}

func DefaultOriginPolicy() OriginPolicyConfig {
	return OriginPolicyConfig{
		TimeoutMS: 10000,
		PassiveHealth: OriginPassiveHealthConfig{
			Enabled: true, FailDurationMS: 30000, MaxFails: 3,
			UnhealthyStatus: []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		},
		Transport: OriginTransportConfig{
			DialTimeoutMS: 3000, TLSHandshakeTimeoutMS: 5000,
			ResponseHeaderTimeoutMS:      10000,
			KeepAliveMaxIdleConnsPerHost: 128, KeepAliveIdleTimeoutMS: 120000, IPVersion: "any",
		},
		Retry: OriginRetryConfig{Retries: 2, TryDurationMS: 5000, TryIntervalMS: 250},
	}
}

func ParseOriginPolicy(governance json.RawMessage) (OriginPolicyConfig, error) {
	policy := DefaultOriginPolicy()
	if len(governance) > 0 && string(governance) != "null" {
		if err := json.Unmarshal(governance, &policy); err != nil {
			return OriginPolicyConfig{}, fmt.Errorf("decode origin governance: %w", err)
		}
	}
	policy = NormalizeOriginPolicy(policy)
	if err := ValidateOriginPolicy(policy); err != nil {
		return OriginPolicyConfig{}, err
	}
	return policy, nil
}

type NodeCacheConfig struct {
	CacheDirectory      string `json:"cache_directory"`
	AutoMaxSize         bool   `json:"auto_max_size"`
	MaxSizeBytes        uint64 `json:"max_size_bytes"`
	MaxDiskUsagePercent int    `json:"max_disk_usage_percent"`
	DebugMode           bool   `json:"debug_mode"`
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
		if origin.Weight < 0 || origin.Priority < 0 {
			return fmt.Errorf("origin %q has invalid weight or priority", origin.Address)
		}
		if current != protocol {
			return errors.New("mixed HTTP and HTTPS origins are not supported in one standard Caddy transport pool")
		}
	}
	switch c.Scheduler {
	case "", "round_robin", "weighted_round_robin", "least_conn", "random", "first", "ip_hash":
	default:
		return fmt.Errorf("unsupported scheduler %q", c.Scheduler)
	}
	return ValidateOriginPolicy(NormalizeOriginPolicy(c.OriginPolicy))
}

func NormalizeOriginPolicy(policy OriginPolicyConfig) OriginPolicyConfig {
	defaults := DefaultOriginPolicy()
	if policy.TimeoutMS == 0 {
		policy.TimeoutMS = defaults.TimeoutMS
	}
	if policy.PassiveHealth.FailDurationMS == 0 {
		policy.PassiveHealth.FailDurationMS = defaults.PassiveHealth.FailDurationMS
	}
	if policy.PassiveHealth.MaxFails == 0 {
		policy.PassiveHealth.MaxFails = defaults.PassiveHealth.MaxFails
	}
	if len(policy.PassiveHealth.UnhealthyStatus) == 0 {
		policy.PassiveHealth.UnhealthyStatus = append([]int(nil), defaults.PassiveHealth.UnhealthyStatus...)
	}
	if policy.Transport.DialTimeoutMS == 0 {
		policy.Transport.DialTimeoutMS = defaults.Transport.DialTimeoutMS
	}
	if policy.Transport.TLSHandshakeTimeoutMS == 0 {
		policy.Transport.TLSHandshakeTimeoutMS = defaults.Transport.TLSHandshakeTimeoutMS
	}
	if policy.Transport.ResponseHeaderTimeoutMS == 0 {
		policy.Transport.ResponseHeaderTimeoutMS = defaults.Transport.ResponseHeaderTimeoutMS
	}
	if policy.Transport.MaxConnsPerHost == 0 {
		policy.Transport.MaxConnsPerHost = defaults.Transport.MaxConnsPerHost
	}
	if policy.Transport.KeepAliveMaxIdleConnsPerHost == 0 {
		policy.Transport.KeepAliveMaxIdleConnsPerHost = defaults.Transport.KeepAliveMaxIdleConnsPerHost
	}
	if policy.Transport.KeepAliveIdleTimeoutMS == 0 {
		policy.Transport.KeepAliveIdleTimeoutMS = defaults.Transport.KeepAliveIdleTimeoutMS
	}
	if policy.Transport.IPVersion == "" {
		policy.Transport.IPVersion = defaults.Transport.IPVersion
	}
	if policy.Retry.Retries == 0 && policy.Retry.TryDurationMS == 0 && policy.Retry.TryIntervalMS == 0 {
		policy.Retry = defaults.Retry
	}
	return policy
}

func ValidateOriginPolicy(policy OriginPolicyConfig) error {
	if policy.TimeoutMS < 1 ||
		policy.PassiveHealth.FailDurationMS < 0 || policy.PassiveHealth.UnhealthyLatencyMS < 0 ||
		policy.Transport.DialTimeoutMS < 0 || policy.Transport.TLSHandshakeTimeoutMS < 0 ||
		policy.Transport.ResponseHeaderTimeoutMS < 0 || policy.Transport.ReadTimeoutMS < 0 ||
		policy.Transport.WriteTimeoutMS < 0 || policy.Transport.MaxConnsPerHost < 0 ||
		policy.Transport.KeepAliveMaxIdleConnsPerHost < 0 || policy.Transport.KeepAliveIdleTimeoutMS < 0 ||
		policy.Retry.Retries < 0 ||
		policy.Retry.TryDurationMS < 0 || policy.Retry.TryIntervalMS < 0 {
		return errors.New("origin durations, connection limits, and retry counts must not be negative")
	}
	if policy.Transport.MaxConnsPerHost > 0 && policy.Transport.KeepAliveMaxIdleConnsPerHost > policy.Transport.MaxConnsPerHost {
		return errors.New("origin keep-alive idle connections per host must not exceed max connections per host")
	}
	if policy.PassiveHealth.Enabled && policy.PassiveHealth.MaxFails < 1 {
		return errors.New("passive health max_fails must be positive")
	}
	for _, status := range policy.PassiveHealth.UnhealthyStatus {
		if status < http.StatusContinue || status > 599 {
			return fmt.Errorf("invalid passive unhealthy status %d", status)
		}
	}
	if policy.Transport.IPVersion != "any" && policy.Transport.IPVersion != "ipv4" && policy.Transport.IPVersion != "ipv6" {
		return fmt.Errorf("unsupported origin IP version policy %q", policy.Transport.IPVersion)
	}
	if (policy.Transport.TLSClientCertificatePEM == "") != (policy.Transport.TLSClientPrivateKeyPEM == "") {
		return errors.New("origin mTLS certificate and private key must be configured together")
	}
	if policy.Transport.MTLSConfigured && policy.Transport.TLSClientCertificatePEM == "" {
		return errors.New("origin mTLS is marked configured but no certificate was supplied")
	}
	for name, values := range policy.Headers {
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("invalid origin header name %q", name)
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("invalid value for origin header %q", name)
			}
		}
	}
	return nil
}

type LogRecord struct {
	ID            uint64          `json:"id"`
	Type          string          `json:"type"`
	SiteID        string          `json:"site_id,omitempty"`
	ConfigVersion uint64          `json:"config_version,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	Payload       json.RawMessage `json:"payload"`
}

type PurgeRequest struct {
	SiteID string   `json:"site_id"`
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
}

type PurgeResult struct {
	Type    string `json:"type"`
	Objects int    `json:"objects"`
}

func (p PurgeRequest) Validate() error {
	if p.SiteID == "" {
		return errors.New("site_id is required")
	}
	switch p.Type {
	case "URL", "PREFIX", "TAG":
		if len(p.Values) == 0 {
			return errors.New("purge values are required")
		}
		for _, value := range p.Values {
			if strings.TrimSpace(value) == "" {
				return errors.New("purge values cannot be empty")
			}
		}
	case "ALL":
		if len(p.Values) != 0 {
			return errors.New("ALL purge does not accept values")
		}
	default:
		return errors.New("invalid purge type")
	}
	return nil
}
