package edgecontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/google/uuid"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/storage/gen/client"
)

const enqueueSiteTombstoneSQL = `INSERT INTO agent_tasks
	(id,node_id,kind,payload,status,idempotency_key,created_at,updated_at)
	SELECT $1,$2,$3,$4,'PENDING',$5,NOW(),NOW()
	WHERE NOT EXISTS (SELECT 1 FROM agent_tasks t
		WHERE t.node_id=$2 AND t.kind=$3 AND t.status IN ('PENDING','RUNNING','SUCCEEDED')
		AND t.payload->>'site_id'=$6 AND COALESCE((t.payload->>'disabled')::boolean,FALSE)
		AND COALESCE((t.payload->>'version')::numeric,0) >= $7::numeric)
	ON CONFLICT (idempotency_key) DO NOTHING`

func (g *Gateway) reconcileSiteVersions(
	ctx context.Context,
	nodeID, clusterID string,
	reported map[string]uint64,
) error {
	if len(reported) == 0 {
		return nil
	}
	type desiredSite struct {
		ID string `db:"id"`
	}
	rows, err := client.Raw[desiredSite](ctx, g.db, `SELECT id FROM sites WHERE cluster_id=$1`, clusterID)
	if err != nil {
		return err
	}
	desired := make(map[string]struct{}, len(rows))
	for _, site := range rows {
		desired[site.ID] = struct{}{}
	}
	for siteID, version := range reported {
		if _, exists := desired[siteID]; exists {
			continue
		}
		if version == math.MaxUint64 {
			return fmt.Errorf("orphan site %s has exhausted its version", siteID)
		}
		if err := g.ensureSiteTombstone(ctx, nodeID, siteID, version); err != nil {
			return fmt.Errorf("disable orphan site %s: %w", siteID, err)
		}
	}
	return nil
}

func (g *Gateway) ensureSiteTombstone(ctx context.Context, nodeID, siteID string, reportedVersion uint64) error {
	version := reportedVersion + 1
	payload, err := json.Marshal(edgeprotocol.SiteConfig{SiteID: siteID, Version: version, Disabled: true})
	if err != nil {
		return err
	}
	idempotencyKey := fmt.Sprintf("%s:site-tombstone:%s:%d", nodeID, siteID, version)
	inserted, err := g.db.RawExec(ctx, enqueueSiteTombstoneSQL,
		uuid.NewString(), nodeID, edgeprotocol.TaskApplySiteConfig, payload, idempotencyKey,
		siteID, strconv.FormatUint(reportedVersion, 10))
	if err != nil {
		return err
	}
	if inserted > 0 {
		g.signal(ctx, "wake", nodeID)
	}
	return nil
}
