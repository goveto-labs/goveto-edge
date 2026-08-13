package outboundhttp

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

type sequenceResolver struct {
	results [][]netip.Addr
	index   int
}

func (r *sequenceResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	result := r.results[r.index]
	if r.index < len(r.results)-1 {
		r.index++
	}
	return result, nil
}

func TestIsPublicAddress(t *testing.T) {
	for _, test := range []struct {
		address string
		public  bool
	}{
		{address: "8.8.8.8", public: true},
		{address: "2606:4700:4700::1111", public: true},
		{address: "127.0.0.1"},
		{address: "10.0.0.1"},
		{address: "169.254.169.254"},
		{address: "192.0.2.1"},
		{address: "::1"},
		{address: "::ffff:127.0.0.1"},
		{address: "fc00::1"},
		{address: "fe80::1"},
	} {
		t.Run(test.address, func(t *testing.T) {
			if got := IsPublicAddress(netip.MustParseAddr(test.address)); got != test.public {
				t.Fatalf("IsPublicAddress(%s) = %t, want %t", test.address, got, test.public)
			}
		})
	}
}

func TestValidateHostRejectsMixedDNSResults(t *testing.T) {
	policy := NewPolicy()
	policy.resolver = staticResolver{
		"mixed.example": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")},
	}
	if err := policy.ValidateHost(context.Background(), "mixed.example"); err == nil {
		t.Fatal("mixed public and private DNS results were accepted")
	}
}

func TestDialContextRevalidatesDNS(t *testing.T) {
	policy := NewPolicy()
	policy.resolver = &sequenceResolver{results: [][]netip.Addr{
		{netip.MustParseAddr("8.8.8.8")},
		{netip.MustParseAddr("127.0.0.1")},
	}}
	if err := policy.ValidateHost(context.Background(), "rebind.example"); err != nil {
		t.Fatalf("initial public result rejected: %v", err)
	}
	if _, err := policy.DialContext(context.Background(), "tcp", "rebind.example:443"); err == nil {
		t.Fatal("connection-time private DNS result was accepted")
	}
}

func TestClientRejectsRedirectToPrivateDestination(t *testing.T) {
	policy := NewPolicy()
	client := policy.Client("https")
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "127.0.0.1"}}
	if err := client.CheckRedirect(request, nil); err == nil {
		t.Fatal("redirect to a private destination was accepted")
	}
}

func TestClientRejectsRedirectDowngrade(t *testing.T) {
	policy := NewPolicy()
	client := policy.Client("https")
	request := &http.Request{URL: &url.URL{Scheme: "http", Host: "8.8.8.8"}}
	if err := client.CheckRedirect(request, nil); err == nil {
		t.Fatal("redirect from HTTPS to HTTP was accepted")
	}
}

func TestClientUsesConfiguredRedirectSchemes(t *testing.T) {
	policy := NewPolicy()
	policy.resolver = staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}
	request := &http.Request{URL: &url.URL{Scheme: "http", Host: "public.example"}}

	if err := policy.Client("https").CheckRedirect(request, nil); err == nil {
		t.Fatal("HTTPS-only client accepted an HTTP redirect")
	}
	if err := policy.Client("http", "https").CheckRedirect(request, nil); err != nil {
		t.Fatalf("client rejected an explicitly allowed HTTP redirect: %v", err)
	}
}

func TestParseAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		wantLen int
	}{
		{name: "empty", raw: "", wantLen: 0},
		{name: "whitespace only", raw: "   ", wantLen: 0},
		{name: "single ipv4", raw: "192.168.0.0/16", wantLen: 1},
		{name: "mixed", raw: "192.168.0.0/16, 10.0.0.0/8, fc00::/7", wantLen: 3},
		{name: "host bits masked", raw: "192.168.1.5/24", wantLen: 1},
		{name: "invalid", raw: "not-a-cidr", wantErr: true},
		{name: "partial invalid", raw: "192.168.0.0/16,bad", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			allowlist, err := ParseAllowlist(test.raw)
			if (err != nil) != test.wantErr {
				t.Fatalf("ParseAllowlist(%q) err = %v, wantErr %t", test.raw, err, test.wantErr)
			}
			if err != nil {
				return
			}
			if len(allowlist) != test.wantLen {
				t.Fatalf("ParseAllowlist(%q) = %d entries, want %d", test.raw, len(allowlist), test.wantLen)
			}
			if test.wantLen > 0 && allowlist[0] != allowlist[0].Masked() {
				t.Fatalf("ParseAllowlist entry %v is not masked", allowlist[0])
			}
		})
	}
}

func TestAllowlistPermitsConfiguredPrivateRange(t *testing.T) {
	allowlist := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}
	policy := NewPolicyWithAllowlist(allowlist)
	policy.resolver = staticResolver{
		"internal.example": {netip.MustParseAddr("192.168.1.10")},
		"other.example":    {netip.MustParseAddr("10.0.0.5")},
		"public.example":   {netip.MustParseAddr("8.8.8.8")},
	}
	if err := policy.ValidateHost(context.Background(), "internal.example"); err != nil {
		t.Fatalf("allowlisted private host rejected: %v", err)
	}
	if err := policy.ValidateHost(context.Background(), "public.example"); err != nil {
		t.Fatalf("public host rejected: %v", err)
	}
	if err := policy.ValidateHost(context.Background(), "other.example"); err == nil {
		t.Fatal("non-allowlisted private host was accepted")
	}
}

func TestDefaultPolicyStillRejectsPrivateRange(t *testing.T) {
	policy := NewPolicy()
	policy.resolver = staticResolver{"internal.example": {netip.MustParseAddr("192.168.1.10")}}
	if err := policy.ValidateHost(context.Background(), "internal.example"); err == nil {
		t.Fatal("default policy accepted a private destination without an allowlist")
	}
}

func TestDefaultPrivateAllowlistCoversRFC1919AndULA(t *testing.T) {
	for _, prefix := range DefaultPrivateAllowlist() {
		if !prefix.Masked().IsValid() {
			t.Fatalf("default allowlist entry invalid: %v", prefix)
		}
	}
	contains := func(addr string) bool {
		a := netip.MustParseAddr(addr)
		for _, prefix := range DefaultPrivateAllowlist() {
			if prefix.Contains(a) {
				return true
			}
		}
		return false
	}
	for _, addr := range []string{"10.1.2.3", "172.20.0.1", "192.168.0.5", "fd00::1"} {
		if !contains(addr) {
			t.Errorf("default allowlist missing %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1", "169.254.169.254", "8.8.8.8"} {
		if contains(addr) {
			t.Errorf("default allowlist must not include %s", addr)
		}
	}
}

// Loopback and link-local (cloud metadata) are blocked even when explicitly
// listed: the operator cannot widen access to self-SSRF / metadata targets.
func TestAllowsNeverUnblocksLoopbackOrMetadata(t *testing.T) {
	policy := NewPolicyWithAllowlist([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("::1/128"),
	})
	policy.resolver = staticResolver{
		"loopback.example": {netip.MustParseAddr("127.0.0.1")},
		"metadata.example": {netip.MustParseAddr("169.254.169.254")},
		"ipv6loop.example": {netip.MustParseAddr("::1")},
	}
	for _, host := range []string{"loopback.example", "metadata.example", "ipv6loop.example"} {
		if err := policy.ValidateHost(context.Background(), host); err == nil {
			t.Fatalf("hard-blocked destination %s was accepted via allowlist", host)
		}
	}
}
