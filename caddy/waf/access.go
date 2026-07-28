package waf

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/oschwald/geoip2-golang"

	"goveto-edge/internal/policy"
)

type compiledAccess struct {
	policy.AccessPolicy
	trusted          []netip.Prefix
	allowed          []netip.Prefix
	blocked          []netip.Prefix
	methodsAllowed   map[string]bool
	methodsBlocked   map[string]bool
	countriesAllowed map[string]bool
	countriesBlocked map[string]bool
	regionsAllowed   map[string]bool
	regionsBlocked   map[string]bool
	referers         map[string]bool
	geo              *geoip2.Reader
}

type accessDecision struct {
	ruleID string
	reason string
}

func compileAccess(input policy.AccessPolicy) (compiledAccess, error) {
	result := compiledAccess{AccessPolicy: input}
	var err error
	if result.trusted, err = parsePrefixes(input.TrustedProxies); err != nil {
		return result, err
	}
	if result.allowed, err = parsePrefixes(input.IPAllowlist); err != nil {
		return result, err
	}
	if result.blocked, err = parsePrefixes(input.IPBlocklist); err != nil {
		return result, err
	}
	result.methodsAllowed = stringSet(input.AllowedMethods)
	result.methodsBlocked = stringSet(input.BlockedMethods)
	result.countriesAllowed = stringSet(input.AllowedCountries)
	result.countriesBlocked = stringSet(input.BlockedCountries)
	result.regionsAllowed = stringSet(input.AllowedRegions)
	result.regionsBlocked = stringSet(input.BlockedRegions)
	result.referers = stringSet(input.AllowedRefererHosts)
	needsGeoIP := len(input.AllowedCountries)+len(input.BlockedCountries)+len(input.AllowedRegions)+len(input.BlockedRegions) > 0
	if needsGeoIP && input.GeoIPDatabase == "" {
		return result, errors.New("GeoIP database is required for country or region restrictions")
	}
	if input.GeoIPDatabase != "" {
		result.geo, err = geoip2.Open(input.GeoIPDatabase)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (a compiledAccess) clientIP(request *http.Request) string {
	direct, err := parseAddress(remoteHost(request.RemoteAddr))
	if err != nil {
		return remoteHost(request.RemoteAddr)
	}
	if !containsPrefix(a.trusted, direct) {
		return direct.String()
	}
	chain := strings.Split(strings.Join(request.Header.Values("X-Forwarded-For"), ","), ",")
	current := direct
	for index := len(chain) - 1; index >= 0; index-- {
		candidate, parseErr := parseAddress(strings.TrimSpace(chain[index]))
		if parseErr != nil {
			return direct.String()
		}
		current = candidate
		if !containsPrefix(a.trusted, candidate) {
			return candidate.String()
		}
	}
	return current.String()
}

func (a compiledAccess) match(request *http.Request, ipText string) *accessDecision {
	ip, _ := parseAddress(ipText)
	allowIP := containsPrefix(a.allowed, ip)
	if !allowIP && len(a.allowed) > 0 {
		return &accessDecision{ruleID: "access:ip-allowlist", reason: "ip_not_allowed"}
	}
	if !allowIP && containsPrefix(a.blocked, ip) {
		return &accessDecision{ruleID: "access:ip-blocklist", reason: "ip_blocked"}
	}
	method := strings.ToUpper(request.Method)
	if len(a.methodsAllowed) > 0 && !a.methodsAllowed[method] {
		return &accessDecision{ruleID: "access:method-allowlist", reason: "method_not_allowed"}
	}
	if a.methodsBlocked[method] {
		return &accessDecision{ruleID: "access:method-blocklist", reason: "method_blocked"}
	}
	if len(a.referers) > 0 {
		raw := strings.TrimSpace(request.Referer())
		if raw == "" && !a.AllowEmptyReferer {
			return &accessDecision{ruleID: "access:referer", reason: "empty_referer"}
		}
		if raw != "" {
			parsed, err := url.Parse(raw)
			host := strings.ToLower(parsed.Hostname())
			if err != nil || !hostAllowed(a.referers, host) {
				return &accessDecision{ruleID: "access:referer", reason: "referer_not_allowed"}
			}
		}
	}
	if a.geo != nil && ip.IsValid() {
		record, err := a.geo.City(net.IP(ip.AsSlice()))
		if err == nil {
			country := strings.ToUpper(record.Country.IsoCode)
			region := ""
			if len(record.Subdivisions) > 0 {
				region = strings.ToUpper(record.Subdivisions[0].IsoCode)
				if country != "" && region != "" {
					region = country + "-" + region
				}
			}
			if len(a.countriesAllowed) > 0 && !a.countriesAllowed[country] {
				return &accessDecision{ruleID: "access:country-allowlist", reason: "country_not_allowed"}
			}
			if a.countriesBlocked[country] {
				return &accessDecision{ruleID: "access:country-blocklist", reason: "country_blocked"}
			}
			if len(a.regionsAllowed) > 0 && !a.regionsAllowed[region] {
				return &accessDecision{ruleID: "access:region-allowlist", reason: "region_not_allowed"}
			}
			if a.regionsBlocked[region] {
				return &accessDecision{ruleID: "access:region-blocklist", reason: "region_blocked"}
			}
		}
	}
	return nil
}

func parsePrefixes(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, len(values))
	for index, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, err
		}
		result[index] = prefix
	}
	return result, nil
}

func parseAddress(value string) (netip.Addr, error) {
	return netip.ParseAddr(strings.Trim(strings.TrimSpace(value), "[]"))
}

func remoteHost(value string) string {
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		return host
	}
	return strings.Trim(value, "[]")
}

func containsPrefix(prefixes []netip.Prefix, address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func hostAllowed(allowed map[string]bool, host string) bool {
	if allowed[host] {
		return true
	}
	for candidate := range allowed {
		if strings.HasPrefix(candidate, "*.") && strings.HasSuffix(host, candidate[1:]) {
			return true
		}
	}
	return false
}
