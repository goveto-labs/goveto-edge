package purge

import (
	"net"
	"testing"
)

func TestPublicIP(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{address: "8.8.8.8", public: true},
		{address: "1.1.1.1", public: true},
		{address: "127.0.0.1", public: false},
		{address: "10.0.0.1", public: false},
		{address: "172.16.0.1", public: false},
		{address: "192.168.1.1", public: false},
		{address: "169.254.1.1", public: false},
		{address: "::1", public: false},
		{address: "fc00::1", public: false},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := publicIP(net.ParseIP(test.address)); got != test.public {
				t.Fatalf("publicIP(%q) = %v, want %v", test.address, got, test.public)
			}
		})
	}
}
