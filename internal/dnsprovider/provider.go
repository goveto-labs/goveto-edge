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
	ListRecords(context.Context, string) ([]Record, error)
	SupportsLines() bool
}

type Credentials struct {
	AccessKeyID     string `json:"access_key_id,omitempty"`
	AccessKeySecret string `json:"access_key_secret,omitempty"`
	APIToken        string `json:"api_token,omitempty"`
}

type Domain struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}

type Line struct {
	Name       string `json:"name"`
	Code       string `json:"code"`
	ParentCode string `json:"parent_code,omitempty"`
	SortOrder  int    `json:"sort_order"`
}

func credentials(kind model.DNSProviderType, raw []byte) (Credentials, error) {
	var value Credentials
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("decode DNS credentials: %w", err)
	}
	switch kind {
	case model.DNSProviderTypeALIYUN:
		if value.AccessKeyID == "" || value.AccessKeySecret == "" {
			return value, fmt.Errorf("Aliyun access_key_id and access_key_secret are required")
		}
	case model.DNSProviderTypeCLOUDFLARE:
		if value.APIToken == "" {
			return value, fmt.Errorf("Cloudflare api_token is required")
		}
	default:
		return value, fmt.Errorf("unsupported DNS provider %q", kind)
	}
	return value, nil
}

func ListDomains(ctx context.Context, kind model.DNSProviderType, raw []byte, client *http.Client) ([]Domain, error) {
	value, err := credentials(kind, raw)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	switch kind {
	case model.DNSProviderTypeALIYUN:
		return (&aliyun{credentials: value, client: client}).ListDomains(ctx)
	case model.DNSProviderTypeCLOUDFLARE:
		return (&cloudflare{token: value.APIToken, client: client}).ListDomains(ctx)
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", kind)
	}
}

func ListLines(ctx context.Context, kind model.DNSProviderType, zone, zoneID string, raw []byte, client *http.Client) ([]Line, error) {
	value, err := credentials(kind, raw)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	switch kind {
	case model.DNSProviderTypeALIYUN:
		return (&aliyun{zone: zone, credentials: value, client: client}).ListLines(ctx)
	case model.DNSProviderTypeCLOUDFLARE:
		return []Line{{Name: "Default", Code: "default", SortOrder: 0}}, nil
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", kind)
	}
}

func New(kind model.DNSProviderType, zone, zoneID string, raw []byte, client *http.Client) (Provider, error) {
	credentials, err := credentials(kind, raw)
	if err != nil {
		return nil, err
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
		return &aliyun{zone: zone, credentials: credentials, client: client}, nil
	case model.DNSProviderTypeCLOUDFLARE:
		if zoneID == "" {
			return nil, fmt.Errorf("Cloudflare zone_id is required")
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
