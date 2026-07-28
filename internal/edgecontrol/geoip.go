package edgecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/geoipdb"
	"goveto-edge/internal/storage/gen/client"
)

const geoIPChunkSize = 512 << 10

const enqueueGeoIPTasksSQL = `INSERT INTO agent_tasks
	(id, node_id, kind, payload, status, idempotency_key, created_at, updated_at)
	SELECT gen_random_uuid(), n.id, $1, $2, 'PENDING', n.id || ':geoip:' || $3,
		NOW(), NOW()
	FROM nodes n JOIN node_credentials c ON c.node_id=n.id
	WHERE n.status <> 'DISABLED' AND c.revoked_at IS NULL
	AND NOT EXISTS (SELECT 1 FROM agent_tasks t WHERE t.node_id=n.id AND t.kind=$1
		AND t.payload->>'sha256'=$3 AND t.status IN ('PENDING','RUNNING','SUCCEEDED'))
	ON CONFLICT (idempotency_key) DO NOTHING`

type geoIPAsset struct {
	mu             sync.RWMutex
	path           string
	poll           time.Duration
	current        edgeprotocol.GeoIPStatus
	data           []byte
	sourceInfo     os.FileInfo
	tasksPending   bool
	publishPending bool
	onUpdate       func(context.Context) error
}

func (g *Gateway) ConfigureGeoIP(path string, poll time.Duration, onUpdate func(context.Context) error) {
	if strings.TrimSpace(path) == "" {
		return
	}
	g.geoIP = &geoIPAsset{path: path, poll: poll, onUpdate: onUpdate}
}

func (g *Gateway) runGeoIP(ctx context.Context) {
	if g.geoIP == nil {
		return
	}
	check := func() {
		_, err := g.refreshGeoIP()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("inspect GeoIP database", "path", g.geoIP.path, "error", err)
			} else {
				slog.Warn("GeoIP database is unavailable", "path", g.geoIP.path)
			}
			return
		}
		g.geoIP.mu.RLock()
		tasksPending := g.geoIP.tasksPending
		g.geoIP.mu.RUnlock()
		if tasksPending {
			if err := g.enqueueGeoIPForAll(ctx); err != nil {
				slog.Warn("enqueue GeoIP synchronization", "error", err)
				return
			}
			g.geoIP.mu.Lock()
			g.geoIP.tasksPending = false
			g.geoIP.mu.Unlock()
		}
		g.geoIP.mu.RLock()
		publishPending := g.geoIP.publishPending
		g.geoIP.mu.RUnlock()
		if publishPending && g.geoIP.onUpdate != nil {
			if err := g.geoIP.onUpdate(ctx); err != nil {
				slog.Warn("republish sites after GeoIP database update", "error", err)
				return
			}
			g.geoIP.mu.Lock()
			g.geoIP.publishPending = false
			g.geoIP.mu.Unlock()
		}
	}
	check()
	ticker := time.NewTicker(g.geoIP.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func (g *Gateway) refreshGeoIP() (bool, error) {
	sourceInfo, err := os.Stat(g.geoIP.path)
	if err != nil {
		return false, err
	}
	g.geoIP.mu.RLock()
	unchanged := sameGeoIPSource(sourceInfo, g.geoIP.sourceInfo)
	g.geoIP.mu.RUnlock()
	if unchanged {
		return false, nil
	}
	metadata, data, err := geoipdb.Inspect(g.geoIP.path)
	if err != nil {
		return false, err
	}
	statusValue := edgeprotocol.GeoIPStatus{SHA256: metadata.SHA256, Size: metadata.Size, BuildEpoch: metadata.BuildEpoch}
	g.geoIP.mu.Lock()
	defer g.geoIP.mu.Unlock()
	if g.geoIP.current.SHA256 == statusValue.SHA256 {
		g.geoIP.sourceInfo = sourceInfo
		return false, nil
	}
	g.geoIP.current = statusValue
	g.geoIP.data = data
	g.geoIP.sourceInfo = sourceInfo
	g.geoIP.tasksPending = true
	g.geoIP.publishPending = true
	slog.Info("GeoIP database version discovered", "sha256", statusValue.SHA256, "size", statusValue.Size, "build_epoch", statusValue.BuildEpoch)
	return true, nil
}

func sameGeoIPSource(current, previous os.FileInfo) bool {
	return current != nil && previous != nil && os.SameFile(current, previous) &&
		current.Size() == previous.Size() && current.ModTime().Equal(previous.ModTime())
}

func inspectGeoIP(path string) (edgeprotocol.GeoIPStatus, error) {
	metadata, _, err := geoipdb.Inspect(path)
	if err != nil {
		return edgeprotocol.GeoIPStatus{}, err
	}
	return edgeprotocol.GeoIPStatus{SHA256: metadata.SHA256, Size: metadata.Size, BuildEpoch: metadata.BuildEpoch}, nil
}

func (g *Gateway) geoIPStatus() edgeprotocol.GeoIPStatus {
	if g.geoIP == nil {
		return edgeprotocol.GeoIPStatus{}
	}
	g.geoIP.mu.RLock()
	defer g.geoIP.mu.RUnlock()
	return g.geoIP.current
}

func (g *Gateway) enqueueGeoIPForAll(ctx context.Context) error {
	current := g.geoIPStatus()
	if current.SHA256 == "" {
		return nil
	}
	payload, _ := json.Marshal(current)
	return g.db.Tx(ctx, func(tx *client.Client) error {
		if _, err := tx.RawExec(ctx, `UPDATE agent_tasks SET status='CANCELLED', cancel_requested_at=NOW(),
			error='superseded by newer GeoIP database', updated_at=NOW()
			WHERE kind=$1 AND status='PENDING' AND payload->>'sha256' <> $2`, edgeprotocol.TaskSyncGeoIP, current.SHA256); err != nil {
			return err
		}
		if _, err := tx.RawExec(ctx, `UPDATE agent_tasks t SET status='PENDING', attempts=0,
			next_attempt_at=NOW(), lease_owner=NULL, lease_until=NULL, heartbeat_at=NULL,
			cancel_requested_at=NULL, result_json=NULL, error=NULL, updated_at=NOW()
			WHERE t.kind=$1 AND t.payload->>'sha256'=$2
			AND t.status IN ('FAILED','DEAD_LETTER','CANCELLED')
			AND EXISTS (SELECT 1 FROM nodes n JOIN node_credentials c ON c.node_id=n.id
				WHERE n.id=t.node_id AND n.status <> 'DISABLED' AND c.revoked_at IS NULL)`, edgeprotocol.TaskSyncGeoIP, current.SHA256); err != nil {
			return err
		}
		_, err := tx.RawExec(ctx, enqueueGeoIPTasksSQL,
			edgeprotocol.TaskSyncGeoIP, payload, current.SHA256)
		return err
	})
}

func (g *Gateway) ensureGeoIPTask(ctx context.Context, nodeID string, agent edgeprotocol.GeoIPStatus) {
	current := g.geoIPStatus()
	if current.SHA256 == "" || agent.SHA256 == current.SHA256 {
		return
	}
	payload, _ := json.Marshal(current)
	idempotencyKey := nodeID + ":geoip:" + current.SHA256
	// A completed task may need to run again if the node lost or rolled back its
	// local asset. Re-arm that durable task before attempting a first insert.
	affected, err := g.db.RawExec(ctx, `UPDATE agent_tasks SET status='PENDING', attempts=0,
		next_attempt_at=NOW(), lease_owner=NULL, lease_until=NULL, heartbeat_at=NULL,
		cancel_requested_at=NULL, result_json=NULL, error=NULL, updated_at=NOW()
		WHERE idempotency_key=$1 AND status IN ('SUCCEEDED','FAILED','DEAD_LETTER','CANCELLED')`, idempotencyKey)
	if err != nil {
		slog.Warn("reconcile agent GeoIP database", "node_id", nodeID, "error", err)
		return
	}
	id := uuid.NewString()
	inserted, err := g.db.RawExec(ctx, `INSERT INTO agent_tasks
		(id,node_id,kind,payload,status,idempotency_key,created_at,updated_at)
		VALUES ($1,$2,$3,$4,'PENDING',$5,NOW(),NOW())
		ON CONFLICT (idempotency_key) DO NOTHING`, id, nodeID, edgeprotocol.TaskSyncGeoIP, payload, idempotencyKey)
	if err != nil {
		slog.Warn("reconcile agent GeoIP database", "node_id", nodeID, "error", err)
		return
	}
	if affected+inserted > 0 {
		g.signal(ctx, "wake", nodeID)
	}
}

func (g *Gateway) DownloadGeoIP(request *edgeprotocol.GeoIPDownloadRequest, stream edgeprotocol.ManagementDownloadGeoIPServer) error {
	certificate, err := peerCertificate(stream.Context())
	if err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	if request.NodeID == "" || CertificateNodeID(certificate) != request.NodeID {
		return status.Error(codes.Unauthenticated, "certificate identity does not match node")
	}
	if err = g.authorize(stream.Context(), request.NodeID, certificate); err != nil {
		return status.Error(codes.PermissionDenied, err.Error())
	}
	if g.geoIP == nil {
		return status.Error(codes.NotFound, "GeoIP database is unavailable")
	}
	g.geoIP.mu.RLock()
	defer g.geoIP.mu.RUnlock()
	current := g.geoIP.current
	if current.SHA256 == "" || request.SHA256 != current.SHA256 {
		return status.Error(codes.NotFound, "requested GeoIP version is unavailable")
	}
	data := g.geoIP.data
	if len(data) == 0 {
		return status.Error(codes.Unavailable, "GeoIP snapshot is unavailable")
	}
	return streamGeoIP(data, current, stream.Send)
}

func streamGeoIP(data []byte, current edgeprotocol.GeoIPStatus, send func(*edgeprotocol.GeoIPChunk) error) error {
	if int64(len(data)) != current.Size {
		return status.Error(codes.Aborted, "GeoIP snapshot size does not match its metadata")
	}
	buffer := make([]byte, geoIPChunkSize)
	var offset int64
	for offset < int64(len(data)) {
		n := copy(buffer, data[offset:])
		if err := send(&edgeprotocol.GeoIPChunk{Offset: offset, Data: append([]byte(nil), buffer[:n]...)}); err != nil {
			return err
		}
		offset += int64(n)
	}
	return nil
}
