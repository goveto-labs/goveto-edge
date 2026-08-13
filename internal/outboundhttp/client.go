// Package outboundhttp provides HTTP clients for user-configured destinations.
package outboundhttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout       = 10 * time.Second
	defaultDialTimeout   = 5 * time.Second
	defaultHeaderTimeout = 5 * time.Second
	maxRedirects         = 5
)

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Policy validates and connects to user-controlled network destinations.
type Policy struct {
	resolver         resolver
	dialer           net.Dialer
	privateAllowlist []netip.Prefix
}

// NewPolicy creates a policy that only permits public destinations.
func NewPolicy() *Policy {
	return &Policy{
		resolver: net.DefaultResolver,
		dialer:   net.Dialer{Timeout: defaultDialTimeout, KeepAlive: 30 * time.Second},
	}
}

// NewPolicyWithAllowlist returns a policy that additionally permits the
// supplied private network ranges. This is the only way a non-public address is
// accepted, so the list must be curated by a platform operator; any cluster
// owner can otherwise only reach public destinations.
func NewPolicyWithAllowlist(allowlist []netip.Prefix) *Policy {
	policy := NewPolicy()
	policy.privateAllowlist = allowlist
	return policy
}

// ParseAllowlist parses a comma-separated list of CIDR prefixes (IPv4/IPv6).
// Empty input returns nil. Invalid entries error so misconfiguration is caught
// at startup instead of silently widening access.
func ParseAllowlist(raw string) ([]netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	allowlist := make([]netip.Prefix, 0, 4)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid destination allowlist entry %q: %w", entry, err)
		}
		allowlist = append(allowlist, prefix.Masked())
	}
	return allowlist, nil
}

// DefaultPrivateAllowlist returns the RFC1919 IPv4 ranges and the IPv6 unique
// local range used when the operator has not configured OUTBOUND_PRIVATE_ALLOWLIST.
// Loopback, link-local (cloud metadata), unspecified and multicast addresses are
// never part of this list and remain blocked regardless of configuration.
func DefaultPrivateAllowlist() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("fc00::/7"),
	}
}

// allows reports whether an address is public or covered by the allowlist.
// Loopback, link-local (including cloud metadata services such as
// 169.254.169.254), unspecified and multicast addresses are always rejected,
// even if they appear in the operator allowlist: these are the self-SSRF and
// metadata-exfiltration vectors SSRF protection must never expose.
func (p *Policy) allows(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	if IsPublicAddress(address) {
		return true
	}
	for _, prefix := range p.privateAllowlist {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// ValidateURL rejects malformed URLs, credentials, unsupported schemes, and non-public destinations.
func (p *Policy) ValidateURL(ctx context.Context, target *url.URL, schemes ...string) error {
	if target == nil || target.Host == "" || target.Hostname() == "" {
		return errors.New("destination URL must include a host")
	}
	if target.User != nil {
		return errors.New("destination URL must not include credentials")
	}
	allowed := false
	for _, scheme := range schemes {
		if strings.EqualFold(target.Scheme, scheme) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("destination URL scheme %q is not allowed", target.Scheme)
	}
	return p.ValidateHost(ctx, target.Hostname())
}

// ValidateHost resolves a host and requires every result to be a public address
// or covered by the configured private allowlist.
func (p *Policy) ValidateHost(ctx context.Context, host string) error {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if host == "" {
		return errors.New("destination host is required")
	}
	addresses, err := p.lookup(ctx, host)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if !p.allows(address) {
			return fmt.Errorf("destination resolves to a non-public address")
		}
	}
	return nil
}

func (p *Policy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	addresses, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve destination host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("destination host has no addresses")
	}
	for index := range addresses {
		addresses[index] = addresses[index].Unmap()
	}
	return addresses, nil
}

// DialContext resolves and checks the destination immediately before each connection.
func (p *Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse destination address: %w", err)
	}
	addresses, err := p.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, candidate := range addresses {
		if !p.allows(candidate) {
			return nil, errors.New("destination resolves to a non-public address")
		}
	}
	var failures []error
	for _, candidate := range addresses {
		connection, dialErr := p.dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		failures = append(failures, dialErr)
	}
	return nil, fmt.Errorf("connect to destination: %w", errors.Join(failures...))
}

// Client returns an HTTP client that revalidates redirects against the allowed
// schemes and pins connections to checked IPs.
func (p *Policy) Client(schemes ...string) *http.Client {
	allowedSchemes := append([]string(nil), schemes...)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           p.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   defaultDialTimeout,
		ResponseHeaderTimeout: defaultHeaderTimeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   defaultTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			return p.ValidateURL(request.Context(), request.URL, allowedSchemes...)
		},
	}
}

// IsPublicAddress reports whether an address is suitable for a user-configured outbound request.
func IsPublicAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
