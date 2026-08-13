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
	resolver resolver
	dialer   net.Dialer
}

// NewPolicy creates a policy backed by the process DNS resolver.
func NewPolicy() *Policy {
	return &Policy{
		resolver: net.DefaultResolver,
		dialer:   net.Dialer{Timeout: defaultDialTimeout, KeepAlive: 30 * time.Second},
	}
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

// ValidateHost resolves a host and requires every result to be a public address.
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
		if !IsPublicAddress(address) {
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
		if !IsPublicAddress(candidate) {
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
