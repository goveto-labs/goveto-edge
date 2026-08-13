package purge

import (
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/storage/gen/model"
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

func TestResourceGuardsReturnNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"missing site", requireSiteInCluster(nil, "cluster-1")},
		{"site in another cluster", requireSiteInCluster(&model.Site{ClusterId: "cluster-2"}, "cluster-1")},
		{"missing purge job", requirePurgeJobInSite(nil, "site-1")},
		{"purge job for another site", requirePurgeJobInSite(&model.PurgeJob{SiteId: "site-2"}, "site-1")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var httpError *echo.HTTPError
			if !errors.As(test.err, &httpError) || httpError.Code != http.StatusNotFound {
				t.Fatalf("error = %#v, want HTTP 404", test.err)
			}
		})
	}
	if err := requireSiteInCluster(&model.Site{ClusterId: "cluster-1"}, "cluster-1"); err != nil {
		t.Fatalf("matching site rejected: %v", err)
	}
	if err := requirePurgeJobInSite(&model.PurgeJob{SiteId: "site-1"}, "site-1"); err != nil {
		t.Fatalf("matching purge job rejected: %v", err)
	}
}
