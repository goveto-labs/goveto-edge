package settings

import "testing"

func TestValidateAgentGatewayPublicAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "hostname", input: " Edge.Example.COM:8443 ", want: "edge.example.com:8443"},
		{name: "IPv4", input: "203.0.113.10:9443", want: "203.0.113.10:9443"},
		{name: "IPv6", input: "[2001:db8::1]:8443", want: "[2001:db8::1]:8443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidateAgentGatewayPublicAddress(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("address = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateAgentGatewayPublicAddressRejectsInvalidValues(t *testing.T) {
	values := []string{
		"",
		"edge.example.com",
		"https://edge.example.com:8443",
		"edge.example.com:8443/path",
		"edge.example.com:0",
		"edge.example.com:65536",
		"-edge.example.com:8443",
		"edge_example.com:8443",
		"2001:db8::1:8443",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			if _, err := ValidateAgentGatewayPublicAddress(value); err == nil {
				t.Fatalf("ValidateAgentGatewayPublicAddress(%q) succeeded", value)
			}
		})
	}
}
