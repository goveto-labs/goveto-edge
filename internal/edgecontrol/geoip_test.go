package edgecontrol

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

func TestInvalidGeoIPUpdateKeepsLastValidVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	if err := os.WriteFile(path, []byte("not a MaxMind database"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := edgeprotocol.GeoIPStatus{SHA256: "previous", Size: 123, BuildEpoch: 456}
	gateway := &Gateway{geoIP: &geoIPAsset{path: path, poll: time.Second, current: previous}}
	if changed, err := gateway.refreshGeoIP(); err == nil || changed {
		t.Fatalf("invalid update result: changed=%v err=%v", changed, err)
	}
	if current := gateway.geoIPStatus(); current != previous {
		t.Fatalf("invalid update replaced valid state: %#v", current)
	}
}

func TestConfigureGeoIPCanBeDisabled(t *testing.T) {
	gateway := &Gateway{}
	gateway.ConfigureGeoIP("  ", time.Second, nil)
	if gateway.geoIP != nil {
		t.Fatal("empty GeoIP path should leave synchronization disabled")
	}
}

func TestGeoIPRefreshKeepsVerifiedByteSnapshot(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "testdata", "GeoIP2-City-Test.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "GeoIP2-City-Test.mmdb")
	if err = os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{geoIP: &geoIPAsset{path: path, poll: time.Second}}
	changed, err := gateway.refreshGeoIP()
	if err != nil || !changed {
		t.Fatalf("initial refresh: changed=%v err=%v", changed, err)
	}
	if !bytes.Equal(gateway.geoIP.data, fixture) {
		t.Fatal("Hub did not retain the verified snapshot")
	}
	if !gateway.geoIP.tasksPending || !gateway.geoIP.publishPending {
		t.Fatal("new snapshot did not request task and publish reconciliation")
	}
	if changed, err = gateway.refreshGeoIP(); err != nil || changed {
		t.Fatalf("unchanged snapshot was reprocessed: changed=%v err=%v", changed, err)
	}
	if err = os.WriteFile(path, []byte("corrupt update"), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err = gateway.refreshGeoIP(); err == nil || changed {
		t.Fatalf("corrupt refresh: changed=%v err=%v", changed, err)
	}
	if !bytes.Equal(gateway.geoIP.data, fixture) {
		t.Fatal("corrupt update replaced the verified snapshot")
	}
}

func TestStreamGeoIPChunksAreOrderedAndBounded(t *testing.T) {
	data := bytes.Repeat([]byte("x"), geoIPChunkSize*2+17)
	status := edgeprotocol.GeoIPStatus{Size: int64(len(data))}
	var chunks []*edgeprotocol.GeoIPChunk
	if err := streamGeoIP(data, status, func(chunk *edgeprotocol.GeoIPChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("unexpected chunk count: %d", len(chunks))
	}
	var combined []byte
	for index, chunk := range chunks {
		if chunk.Offset != int64(len(combined)) || len(chunk.Data) == 0 || len(chunk.Data) > geoIPChunkSize {
			t.Fatalf("invalid chunk %d: offset=%d size=%d", index, chunk.Offset, len(chunk.Data))
		}
		combined = append(combined, chunk.Data...)
	}
	if !bytes.Equal(combined, data) {
		t.Fatal("streamed chunks did not reconstruct the snapshot")
	}
}

func TestGeoIPEnqueueSQLIncludesOfflineNodesAndDurableDeduplication(t *testing.T) {
	for _, fragment := range []string{
		"n.status <> 'DISABLED'",
		"c.revoked_at IS NULL",
		"t.payload->>'sha256'=$3",
		"ON CONFLICT (idempotency_key) DO NOTHING",
	} {
		if !strings.Contains(enqueueGeoIPTasksSQL, fragment) {
			t.Fatalf("GeoIP enqueue SQL missing %q: %s", fragment, enqueueGeoIPTasksSQL)
		}
	}
	if strings.Contains(enqueueGeoIPTasksSQL, "n.status = 'ONLINE'") || strings.Contains(enqueueGeoIPTasksSQL, "timeout_at") {
		t.Fatalf("GeoIP tasks must remain queued for offline nodes without a deadline: %s", enqueueGeoIPTasksSQL)
	}
}
