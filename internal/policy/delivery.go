package policy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
)

type DeliveryPolicy struct {
	RequestHeaders  []HeaderRule       `json:"request_headers"`
	ResponseHeaders []HeaderRule       `json:"response_headers"`
	Rewrites        []RewriteRule      `json:"rewrites"`
	Redirects       []RedirectRule     `json:"redirects"`
	CORS            CORSConfig         `json:"cors"`
	Protocols       ProtocolConfig     `json:"protocols"`
	ErrorPages      []ErrorPage        `json:"error_pages"`
	OriginPrefix    string             `json:"origin_prefix"`
	OriginPools     []PathOriginPool   `json:"origin_pools"`
	Splits          []TrafficSplitRule `json:"splits"`
	Maintenance     MaintenanceConfig  `json:"maintenance"`
}

type HeaderRule struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
}

type RewriteRule struct {
	Path        string `json:"path"`
	Replacement string `json:"replacement"`
}

type RedirectRule struct {
	Path     string `json:"path"`
	Location string `json:"location"`
	Status   int    `json:"status"`
}

type CORSConfig struct {
	Enabled          bool     `json:"enabled"`
	AllowOrigins     []string `json:"allow_origins"`
	AllowMethods     []string `json:"allow_methods"`
	AllowHeaders     []string `json:"allow_headers"`
	ExposeHeaders    []string `json:"expose_headers"`
	AllowCredentials bool     `json:"allow_credentials"`
	MaxAgeSeconds    int      `json:"max_age_seconds,omitempty"`
}

type ProtocolConfig struct {
	WebSocket   bool `json:"websocket"`
	GRPC        bool `json:"grpc"`
	HTTPUpgrade bool `json:"http_upgrade"`
}

type ErrorPage struct {
	Statuses    []int  `json:"statuses"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body"`
}

type PathOriginPool struct {
	Name      string           `json:"name"`
	Paths     []string         `json:"paths"`
	Scheduler string           `json:"scheduler,omitempty"`
	Origins   []DeliveryOrigin `json:"origins"`
}

type DeliveryOrigin struct {
	Protocol   string `json:"protocol"`
	Address    string `json:"address"`
	HostHeader string `json:"host_header,omitempty"`
	Weight     int    `json:"weight,omitempty"`
}

type TrafficSplitRule struct {
	Name       string `json:"name"`
	Pool       string `json:"pool"`
	HeaderName string `json:"header_name,omitempty"`
	CookieName string `json:"cookie_name,omitempty"`
	Value      string `json:"value,omitempty"`
	Percentage int    `json:"percentage,omitempty"`
}

type MaintenanceConfig struct {
	Enabled     bool   `json:"enabled"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body,omitempty"`
}

func DefaultDeliveryPolicy() DeliveryPolicy {
	return DeliveryPolicy{
		RequestHeaders:  []HeaderRule{},
		ResponseHeaders: []HeaderRule{},
		Rewrites:        []RewriteRule{},
		Redirects:       []RedirectRule{},
		CORS: CORSConfig{
			AllowOrigins:  []string{},
			AllowMethods:  []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions},
			AllowHeaders:  []string{},
			ExposeHeaders: []string{},
		},
		Protocols:   ProtocolConfig{WebSocket: true},
		ErrorPages:  []ErrorPage{},
		OriginPools: []PathOriginPool{},
		Splits:      []TrafficSplitRule{},
		Maintenance: MaintenanceConfig{Status: http.StatusServiceUnavailable, ContentType: "text/html; charset=utf-8", Body: "Service temporarily unavailable"},
	}
}

func (p *DeliveryPolicy) NormalizeAndValidate() error {
	defaults := DefaultDeliveryPolicy()
	if p.Maintenance.Status == 0 {
		p.Maintenance.Status = defaults.Maintenance.Status
	}
	if p.Maintenance.ContentType == "" {
		p.Maintenance.ContentType = defaults.Maintenance.ContentType
	}
	if p.Maintenance.Body == "" {
		p.Maintenance.Body = defaults.Maintenance.Body
	}
	if p.CORS.Enabled && len(p.CORS.AllowMethods) == 0 {
		p.CORS.AllowMethods = defaults.CORS.AllowMethods
	}
	if p.OriginPrefix != "" {
		p.OriginPrefix = strings.TrimSuffix(strings.TrimSpace(p.OriginPrefix), "/")
		if !validAbsolutePath(p.OriginPrefix) {
			return errors.New("origin_prefix must be an absolute path")
		}
	}
	if err := validateHeaders("request_headers", p.RequestHeaders); err != nil {
		return err
	}
	if err := validateHeaders("response_headers", p.ResponseHeaders); err != nil {
		return err
	}
	for i := range p.Rewrites {
		p.Rewrites[i].Path = strings.TrimSpace(p.Rewrites[i].Path)
		p.Rewrites[i].Replacement = strings.TrimSpace(p.Rewrites[i].Replacement)
		if !validPathMatcher(p.Rewrites[i].Path) || !validAbsolutePath(p.Rewrites[i].Replacement) {
			return fmt.Errorf("rewrites[%d] requires an absolute path matcher and replacement", i)
		}
	}
	for i := range p.Redirects {
		rule := &p.Redirects[i]
		rule.Path, rule.Location = strings.TrimSpace(rule.Path), strings.TrimSpace(rule.Location)
		if !validPathMatcher(rule.Path) {
			return fmt.Errorf("redirects[%d].path must be an absolute path matcher", i)
		}
		if !validRedirectLocation(rule.Location) {
			return fmt.Errorf("redirects[%d].location must be an absolute path or HTTP(S) URL", i)
		}
		if rule.Status == 0 {
			rule.Status = http.StatusFound
		}
		if rule.Status != 301 && rule.Status != 302 && rule.Status != 307 && rule.Status != 308 {
			return fmt.Errorf("redirects[%d].status is unsupported", i)
		}
	}
	if err := p.normalizeCORS(); err != nil {
		return err
	}
	seenStatuses := map[int]bool{}
	for i := range p.ErrorPages {
		page := &p.ErrorPages[i]
		if page.ContentType == "" {
			page.ContentType = "text/html; charset=utf-8"
		}
		if page.Body == "" || len(page.Statuses) == 0 {
			return fmt.Errorf("error_pages[%d] requires statuses and body", i)
		}
		for _, status := range page.Statuses {
			if status < 400 || status > 599 || seenStatuses[status] {
				return fmt.Errorf("error_pages[%d] contains invalid or duplicate status %d", i, status)
			}
			seenStatuses[status] = true
		}
		sort.Ints(page.Statuses)
	}
	poolNames := map[string]bool{}
	for i := range p.OriginPools {
		pool := &p.OriginPools[i]
		pool.Name = strings.TrimSpace(pool.Name)
		if pool.Name == "" || poolNames[pool.Name] {
			return fmt.Errorf("origin_pools[%d] has an empty or duplicate name", i)
		}
		poolNames[pool.Name] = true
		if len(pool.Paths) == 0 || len(pool.Origins) == 0 {
			return fmt.Errorf("origin_pools[%d] requires paths and origins", i)
		}
		for _, path := range pool.Paths {
			if !validPathMatcher(path) {
				return fmt.Errorf("origin_pools[%d] contains an invalid path matcher", i)
			}
		}
		if pool.Scheduler == "" {
			pool.Scheduler = "round_robin"
		}
		if !validScheduler(pool.Scheduler) {
			return fmt.Errorf("origin_pools[%d] has an unsupported scheduler", i)
		}
		for oi := range pool.Origins {
			origin := &pool.Origins[oi]
			origin.Protocol = strings.ToLower(strings.TrimSpace(origin.Protocol))
			origin.Address = strings.TrimSpace(origin.Address)
			origin.HostHeader = strings.TrimSpace(origin.HostHeader)
			if origin.Protocol != "http" && origin.Protocol != "https" || !validOriginAddress(origin.Address) {
				return fmt.Errorf("origin_pools[%d].origins[%d] is invalid", i, oi)
			}
			if origin.HostHeader != "" && !httpguts.ValidHostHeader(origin.HostHeader) {
				return fmt.Errorf("origin_pools[%d].origins[%d].host_header is invalid", i, oi)
			}
			if origin.Weight <= 0 {
				origin.Weight = 1
			}
		}
	}
	splitNames := map[string]bool{}
	for i := range p.Splits {
		rule := &p.Splits[i]
		rule.Name, rule.Pool = strings.TrimSpace(rule.Name), strings.TrimSpace(rule.Pool)
		if rule.Name == "" || splitNames[rule.Name] || !poolNames[rule.Pool] {
			return fmt.Errorf("splits[%d] requires a name and existing pool", i)
		}
		splitNames[rule.Name] = true
		rule.HeaderName = http.CanonicalHeaderKey(strings.TrimSpace(rule.HeaderName))
		rule.CookieName = strings.TrimSpace(rule.CookieName)
		if rule.HeaderName != "" && !httpguts.ValidHeaderFieldName(rule.HeaderName) {
			return fmt.Errorf("splits[%d].header_name is invalid", i)
		}
		if rule.CookieName != "" && !validCookieName(rule.CookieName) {
			return fmt.Errorf("splits[%d].cookie_name is invalid", i)
		}
		if rule.HeaderName == "" && rule.CookieName == "" && rule.Percentage == 0 {
			return fmt.Errorf("splits[%d] requires a header, cookie, or percentage", i)
		}
		if rule.Percentage < 0 || rule.Percentage > 100 {
			return fmt.Errorf("splits[%d].percentage must be between 0 and 100", i)
		}
	}
	if p.Maintenance.Enabled && (p.Maintenance.Status < 400 || p.Maintenance.Status > 599) {
		return errors.New("maintenance.status must be between 400 and 599")
	}
	return nil
}

func validateHeaders(field string, rules []HeaderRule) error {
	for i := range rules {
		rule := &rules[i]
		rule.Operation = strings.ToUpper(strings.TrimSpace(rule.Operation))
		rule.Name = http.CanonicalHeaderKey(strings.TrimSpace(rule.Name))
		if rule.Operation == "" {
			rule.Operation = "SET"
		}
		if rule.Operation != "SET" && rule.Operation != "ADD" && rule.Operation != "DELETE" {
			return fmt.Errorf("%s[%d].operation is unsupported", field, i)
		}
		if !httpguts.ValidHeaderFieldName(rule.Name) || isHopByHopHeader(rule.Name) {
			return fmt.Errorf("%s[%d].name is invalid or reserved", field, i)
		}
		if rule.Operation != "DELETE" && !httpguts.ValidHeaderFieldValue(rule.Value) {
			return fmt.Errorf("%s[%d].value is invalid", field, i)
		}
	}
	return nil
}

func (p *DeliveryPolicy) normalizeCORS() error {
	if !p.CORS.Enabled {
		return nil
	}
	if len(p.CORS.AllowOrigins) == 0 {
		return errors.New("cors.allow_origins is required when CORS is enabled")
	}
	for _, origin := range p.CORS.AllowOrigins {
		if origin == "*" {
			if p.CORS.AllowCredentials {
				return errors.New("cors wildcard origin cannot be used with credentials")
			}
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("invalid CORS origin %q", origin)
		}
	}
	for i, method := range p.CORS.AllowMethods {
		p.CORS.AllowMethods[i] = strings.ToUpper(strings.TrimSpace(method))
		if !httpguts.ValidHeaderFieldName(p.CORS.AllowMethods[i]) {
			return fmt.Errorf("invalid CORS method %q", method)
		}
	}
	for _, group := range [][]string{p.CORS.AllowHeaders, p.CORS.ExposeHeaders} {
		for _, header := range group {
			if !httpguts.ValidHeaderFieldName(header) {
				return fmt.Errorf("invalid CORS header %q", header)
			}
		}
	}
	if p.CORS.MaxAgeSeconds < 0 || p.CORS.MaxAgeSeconds > 86400 {
		return errors.New("cors.max_age_seconds must be between 0 and 86400")
	}
	return nil
}

func validAbsolutePath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "\r\n")
}

func validPathMatcher(value string) bool {
	return validAbsolutePath(value) && !strings.Contains(value, "..")
}

func validRedirectLocation(value string) bool {
	if validAbsolutePath(value) {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func validScheduler(value string) bool {
	switch value {
	case "round_robin", "weighted_round_robin", "least_conn", "random", "first", "ip_hash":
		return true
	default:
		return false
	}
}

func validCookieName(value string) bool {
	return value != "" && regexp.MustCompile(`^[!#$%&'*+.^_`+"`"+`|~0-9A-Za-z-]+$`).MatchString(value)
}

func validOriginAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" || strings.ContainsAny(host, "\r\n") {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number > 0 && number <= 65535
}

func isHopByHopHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}
