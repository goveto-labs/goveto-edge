package types

import (
	"encoding/json"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

// DNSJob is the public, relation-free representation of a DNS sync job.
// GCORM models must not be returned directly because their relation graph
// makes OpenAPI recursively expand clusters, sites and jobs.
type DNSJob struct {
	ID          string              `json:"id"`
	Action      model.DNSSyncAction `json:"action"`
	Status      model.JobStatus     `json:"status"`
	Attempts    int                 `json:"attempts"`
	MaxAttempts int                 `json:"maxAttempts"`
	Result      *DNSJobResult       `json:"resultJson"`
	CreatedAt   time.Time           `json:"createdAt"`
}

type DNSJobResult struct {
	Error string `json:"error,omitempty"`
}

// DNSLine is the public representation of a provider routing line.
type DNSLine struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderCode string `json:"providerCode"`
}

type DNSConfig struct {
	PrimaryHostname *string            `json:"primary_hostname"`
	Provider        *DNSProviderConfig `json:"provider"`
}

type DNSProviderConfig struct {
	Type                  model.DNSProviderType `json:"type"`
	Zone                  string                `json:"zone"`
	ZoneID                *string               `json:"zone_id"`
	DefaultTTL            int                   `json:"default_ttl"`
	Proxied               bool                  `json:"proxied"`
	Enabled               bool                  `json:"enabled"`
	CredentialsConfigured bool                  `json:"credentials_configured"`
}

func NewDNSConfig(hostname *string, provider *model.DNSProviderConfig) DNSConfig {
	result := DNSConfig{PrimaryHostname: hostname}
	if provider != nil {
		result.Provider = &DNSProviderConfig{Type: provider.Provider, Zone: provider.Zone, ZoneID: provider.ZoneId, DefaultTTL: provider.DefaultTtl, Proxied: provider.Proxied, Enabled: provider.Enabled, CredentialsConfigured: provider.CredentialsEncrypted != ""}
	}
	return result
}

func NewDNSLine(line *model.DNSLine) DNSLine {
	return DNSLine{ID: line.Id, Name: line.Name, ProviderCode: line.ProviderCode}
}

func NewDNSJob(job *model.DNSSyncJob) DNSJob {
	response := DNSJob{
		ID: job.Id, Action: job.Action, Status: job.Status,
		Attempts: job.Attempts, MaxAttempts: job.MaxAttempts, CreatedAt: job.CreatedAt,
	}
	if job.ResultJson != nil {
		var result DNSJobResult
		if json.Unmarshal(*job.ResultJson, &result) == nil {
			response.Result = &result
		}
	}
	return response
}
