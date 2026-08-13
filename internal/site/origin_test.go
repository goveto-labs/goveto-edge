package site

import "testing"

func TestNormalizeHostHeader(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty", input: " \t", want: ""},
		{name: "hostname", input: " origin.example.com ", want: "origin.example.com"},
		{name: "host and port", input: "origin.example.com:8443", want: "origin.example.com:8443"},
		{name: "ipv6 literal", input: "[2001:db8::1]:443", want: "[2001:db8::1]:443"},
		{name: "crlf", input: "origin.example.com\r\nX-Injected: true", wantErr: true},
		{name: "path", input: "origin.example.com/path", wantErr: true},
		{name: "internal whitespace", input: "origin .example.com", wantErr: true},
		{name: "control character", input: "origin.example.com\x00", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeHostHeader(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("NormalizeHostHeader(%q) succeeded with %q", test.input, got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("NormalizeHostHeader(%q) = %q, %v; want %q, nil", test.input, got, err, test.want)
			}
		})
	}
}
