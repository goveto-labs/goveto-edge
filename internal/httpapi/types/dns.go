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
