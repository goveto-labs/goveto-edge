package securitystate

import "time"

// TemporaryBlock is the shared Redis value stored under site/global block
// keys. Both operator-created and automatic blocks use this schema.
type TemporaryBlock struct {
	Scope     string    `json:"scope"`
	SiteID    string    `json:"site_id,omitempty"`
	Address   string    `json:"address"`
	Reason    string    `json:"reason,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}
