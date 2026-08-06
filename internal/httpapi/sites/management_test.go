package sites

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/storage/gen/model"
)

func TestPrepareCloneBundleDropsDomainBoundTLS(t *testing.T) {
	bundle := siteBundle{
		Name: "source", Domains: []string{"source.example.com"}, CertificateIDs: []string{"certificate-id"},
		Listener: edgeprotocol.ListenerConfig{
			RedirectHTTPToHTTPS: true, HTTPSEnabled: true, HTTP2Enabled: true, HTTP3Enabled: true,
			HSTSEnabled: true, OCSPStaplingEnabled: true,
		},
	}
	clone := prepareCloneBundle(bundle, cloneRequest{Name: " copied ", Domains: []string{"copy.example.com"}})
	if clone.Name != "copied" || len(clone.CertificateIDs) != 0 {
		t.Fatalf("clone retained source identity or certificates: %#v", clone)
	}
	if !clone.Listener.HTTPEnabled || clone.Listener.HTTPPort != 80 {
		t.Fatalf("clone did not retain a reachable HTTP listener: %#v", clone.Listener)
	}
	if clone.Listener.RedirectHTTPToHTTPS || clone.Listener.HTTPSEnabled || clone.Listener.HTTP2Enabled || clone.Listener.HTTP3Enabled || clone.Listener.HSTSEnabled || clone.Listener.OCSPStaplingEnabled {
		t.Fatalf("clone retained TLS settings without a matching certificate: %#v", clone.Listener)
	}
}

func TestCreateSiteBundleRejectsInvalidStatusBeforeDatabaseWrite(t *testing.T) {
	bundle := validManagementBundle()
	bundle.Status = model.SiteStatus("DELETED")
	_, err := createSiteBundle(context.Background(), nil, "cluster", "creator", bundle)
	if err == nil || !strings.Contains(err.Error(), "invalid site status") {
		t.Fatalf("invalid status error = %v", err)
	}
}

func TestCreateSiteBundleRejectsNegativeOriginPriorityBeforeDatabaseWrite(t *testing.T) {
	bundle := validManagementBundle()
	bundle.Origins[0].Priority = -1
	_, err := createSiteBundle(context.Background(), nil, "cluster", "creator", bundle)
	if err == nil || !strings.Contains(err.Error(), "priority") {
		t.Fatalf("negative priority error = %v", err)
	}
}

func TestCreateSiteBundleRejectsInvalidImportedPolicyBeforeDatabaseWrite(t *testing.T) {
	bundle := validManagementBundle()
	bundle.Cache = json.RawMessage(`{"ttl":{"default_seconds":-1}}`)
	_, err := createSiteBundle(context.Background(), nil, "cluster", "creator", bundle)
	if err == nil || !strings.Contains(err.Error(), "invalid cache policy") {
		t.Fatalf("invalid cache policy error = %v", err)
	}

	bundle = validManagementBundle()
	bundle.WAF = json.RawMessage(`{"enabled":true,"mode":"INVALID"}`)
	_, err = createSiteBundle(context.Background(), nil, "cluster", "creator", bundle)
	if err == nil || !strings.Contains(err.Error(), "invalid WAF policy") {
		t.Fatalf("invalid WAF policy error = %v", err)
	}
}

func validManagementBundle() siteBundle {
	return siteBundle{
		Name: "site", Domains: []string{"site.example.com"},
		Origins:      []originInput{{Protocol: model.OriginProtocolHTTP, Address: "origin.example.com:80", Weight: 1}},
		OriginPolicy: edgeprotocol.DefaultOriginPolicy(),
	}
}
