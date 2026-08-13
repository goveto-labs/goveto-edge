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
