package sites

import "testing"

func TestNormalizeTemporaryBlock(t *testing.T) {
	for _, test := range []struct {
		scope   string
		address string
		want    string
	}{
		{scope: "", address: "192.0.2.10", want: "SITE:192.0.2.10"},
		{scope: "global", address: "[2001:db8::1]", want: "GLOBAL:2001:db8::1"},
		{scope: "SITE", address: "::ffff:192.0.2.10", want: "SITE:192.0.2.10"},
	} {
		scope, address, err := normalizeTemporaryBlock(test.scope, test.address)
		if err != nil {
			t.Fatal(err)
		}
		if got := scope + ":" + address.String(); got != test.want {
			t.Fatalf("normalizeTemporaryBlock(%q, %q)=%q want=%q", test.scope, test.address, got, test.want)
		}
	}
}

func TestNormalizeTemporaryBlockRejectsCIDRAndUnknownScope(t *testing.T) {
	for _, test := range []struct{ scope, address string }{
		{scope: "CLUSTER", address: "192.0.2.10"},
		{scope: "SITE", address: "192.0.2.0/24"},
		{scope: "SITE", address: "not-an-ip"},
	} {
		if _, _, err := normalizeTemporaryBlock(test.scope, test.address); err == nil {
			t.Fatalf("expected %q %q to be rejected", test.scope, test.address)
		}
	}
}
