// Package dnsprovider contains DNS vendor adapters used by the control plane.
package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"goveto-edge/internal/storage/gen/model"
)

type Record struct {
	ID       string
	Hostname string
	Type     model.DNSRecordType
	Value    string
	Line     string
	TTL      int
	Proxied  bool
}

type Provider interface {
	Upsert(context.Context, Record) (string, error)
	Delete(context.Context, Record) error
	SupportsLines() bool
}

type Credentials struct {
	AccessKeyID     string `json:"access_key_id,omitempty"`
	AccessKeySecret string `json:"access_key_secret,omitempty"`
	APIToken        string `json:"api_token,omitempty"`
}

func New(kind model.DNSProviderType, zone, zoneID string, raw []byte, client *http.Client) (Provider, error) {
	var credentials Credentials
	if err := json.Unmarshal(raw, &credentials); err != nil {
		return nil, fmt.Errorf("decode DNS credentials: %w", err)
	}
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if zone == "" {
		return nil, fmt.Errorf("DNS zone is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	switch kind {
	case model.DNSProviderTypeALIYUN:
		if credentials.AccessKeyID == "" || credentials.AccessKeySecret == "" {
			return nil, fmt.Errorf("Aliyun access_key_id and access_key_secret are required")
		}
		return &aliyun{zone: zone, credentials: credentials, client: client}, nil
	case model.DNSProviderTypeCLOUDFLARE:
		if credentials.APIToken == "" || zoneID == "" {
			return nil, fmt.Errorf("Cloudflare api_token and zone_id are required")
		}
		return &cloudflare{zone: zone, zoneID: zoneID, token: credentials.APIToken, client: client}, nil
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", kind)
	}
}

func RelativeName(hostname, zone string) (string, error) {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if hostname == zone {
		return "@", nil
	}
	suffix := "." + zone
	if !strings.HasSuffix(hostname, suffix) {
		return "", fmt.Errorf("hostname %q is outside DNS zone %q", hostname, zone)
	}
	return strings.TrimSuffix(hostname, suffix), nil
}
