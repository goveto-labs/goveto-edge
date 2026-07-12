package dns

import "testing"

func TestHostname(t *testing.T) {
	tests := []struct {
		input string
		want  string
		valid bool
	}{
		{input: "Edge.Example.com.", want: "edge.example.com", valid: true},
		{input: "bücher.example", want: "xn--bcher-kva.example", valid: true},
		{input: "*.example.com", valid: false},
		{input: "bad_label.example.com", valid: false},
		{input: "127.0.0.1", valid: false},
		{input: "bad..example.com", valid: false},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := hostname(test.input)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("hostname(%q) = %q, %v; want %q", test.input, got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("hostname(%q) unexpectedly returned %q", test.input, got)
			}
		})
	}
}

func TestValidProviderCode(t *testing.T) {
	for _, code := range []string{"telecom", "cn_mobile", "oversea-1"} {
		if !validProviderCode(code) {
			t.Fatalf("validProviderCode(%q) = false", code)
		}
	}
	for _, code := range []string{"", "China Telecom", "telecom/1"} {
		if validProviderCode(code) {
			t.Fatalf("validProviderCode(%q) = true", code)
		}
	}
}
