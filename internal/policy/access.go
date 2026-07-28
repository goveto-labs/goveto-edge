package policy

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

type AccessPolicy struct {
	Enabled               bool     `json:"enabled"`
	Mode                  string   `json:"mode"`
	StatusCode            int      `json:"status_code"`
	TrustedProxies        []string `json:"trusted_proxies"`
	IPAllowlist           []string `json:"ip_allowlist"`
	IPBlocklist           []string `json:"ip_blocklist"`
	AllowedCountries      []string `json:"allowed_countries"`
	BlockedCountries      []string `json:"blocked_countries"`
	AllowedRegions        []string `json:"allowed_regions"`
	BlockedRegions        []string `json:"blocked_regions"`
	AllowedMethods        []string `json:"allowed_methods"`
	BlockedMethods        []string `json:"blocked_methods"`
	AllowedRefererHosts   []string `json:"allowed_referer_hosts"`
	AllowEmptyReferer     bool     `json:"allow_empty_referer"`
	GeoIPDatabase         string   `json:"geoip_database,omitempty" swaggerignore:"true"`
	TemporaryBlocks       bool     `json:"temporary_blocks"`
	TemporaryBlockFailure string   `json:"temporary_block_failure"`
}

func DefaultAccessPolicy() AccessPolicy {
	return AccessPolicy{
		Mode: "BLOCK", StatusCode: http.StatusForbidden, AllowEmptyReferer: true,
		TemporaryBlockFailure: "OPEN",
		TrustedProxies:        []string{}, IPAllowlist: []string{}, IPBlocklist: []string{},
		AllowedCountries: []string{}, BlockedCountries: []string{},
		AllowedRegions: []string{}, BlockedRegions: []string{},
		AllowedMethods: []string{}, BlockedMethods: []string{}, AllowedRefererHosts: []string{},
	}
}

func (p *AccessPolicy) NormalizeAndValidate() error {
	p.Mode = strings.ToUpper(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = "BLOCK"
	}
	if p.Mode != "BLOCK" && p.Mode != "MONITOR" {
		return errors.New("access mode must be BLOCK or MONITOR")
	}
	if p.StatusCode == 0 {
		p.StatusCode = http.StatusForbidden
	}
	if p.StatusCode < 400 || p.StatusCode > 599 {
		return errors.New("access status_code must be between 400 and 599")
	}
	p.TemporaryBlockFailure = strings.ToUpper(strings.TrimSpace(p.TemporaryBlockFailure))
	if p.TemporaryBlockFailure == "" {
		p.TemporaryBlockFailure = "OPEN"
	}
	if p.TemporaryBlockFailure != "OPEN" && p.TemporaryBlockFailure != "CLOSED" {
		return errors.New("temporary_block_failure must be OPEN or CLOSED")
	}
	for name, values := range map[string]*[]string{
		"trusted_proxies": &p.TrustedProxies, "ip_allowlist": &p.IPAllowlist, "ip_blocklist": &p.IPBlocklist,
	} {
		normalized, err := normalizePrefixes(*values)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*values = normalized
	}
	p.AllowedCountries = normalizeCodes(p.AllowedCountries)
	p.BlockedCountries = normalizeCodes(p.BlockedCountries)
	p.AllowedRegions = normalizeCodes(p.AllowedRegions)
	p.BlockedRegions = normalizeCodes(p.BlockedRegions)
	for _, country := range append(append([]string{}, p.AllowedCountries...), p.BlockedCountries...) {
		if len(country) != 2 {
			return fmt.Errorf("country code %q must contain two characters", country)
		}
	}
	p.AllowedMethods = normalizeCodes(p.AllowedMethods)
	p.BlockedMethods = normalizeCodes(p.BlockedMethods)
	for _, method := range append(append([]string{}, p.AllowedMethods...), p.BlockedMethods...) {
		if !validMethod(method) {
			return fmt.Errorf("invalid HTTP method %q", method)
		}
	}
	hosts := make([]string, 0, len(p.AllowedRefererHosts))
	for _, raw := range p.AllowedRefererHosts {
		host := strings.ToLower(strings.TrimSpace(raw))
		if strings.Contains(host, "://") {
			parsed, err := url.Parse(host)
			if err != nil || parsed.Hostname() == "" {
				return fmt.Errorf("invalid referer host %q", raw)
			}
			host = parsed.Hostname()
		}
		if host == "" || strings.ContainsAny(host, "/?#@") {
			return fmt.Errorf("invalid referer host %q", raw)
		}
		hosts = append(hosts, host)
	}
	p.AllowedRefererHosts = uniqueSorted(hosts)
	p.GeoIPDatabase = strings.TrimSpace(p.GeoIPDatabase)
	return nil
}

// NormalizeAndValidatePublic ignores the legacy client-controlled GeoIP path.
func (p *AccessPolicy) NormalizeAndValidatePublic() error {
	p.GeoIPDatabase = ""
	return p.NormalizeAndValidate()
}

func normalizePrefixes(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if address, err := netip.ParseAddr(value); err == nil {
			bits := 128
			if address.Is4() {
				bits = 32
			}
			result = append(result, netip.PrefixFrom(address, bits).String())
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid IP or CIDR %q", value)
		}
		result = append(result, prefix.Masked().String())
	}
	return uniqueSorted(result), nil
}

func normalizeCodes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.ToUpper(strings.TrimSpace(value)); value != "" {
			result = append(result, value)
		}
	}
	return uniqueSorted(result)
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	if result == nil {
		return []string{}
	}
	return result
}

func validMethod(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character <= 32 || character >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", character) {
			return false
		}
	}
	return true
}
