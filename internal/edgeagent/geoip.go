package edgeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/geoipdb"
)

const maxGeoIPDatabaseSize = geoipdb.MaxSize

type GeoIPStore struct {
	dir       string
	path      string
	metaPath  string
	reload    func() error
	installMu sync.Mutex
	statusMu  sync.Mutex
	loaded    bool
	status    edgeprotocol.GeoIPStatus
	dbInfo    os.FileInfo
	metaInfo  os.FileInfo
}

func NewGeoIPStore(dataDir string, configs *ConfigManager) *GeoIPStore {
	dir := filepath.Join(dataDir, "geoip")
	store := &GeoIPStore{dir: dir, path: filepath.Join(dir, "GeoLite2-City.mmdb"), metaPath: filepath.Join(dir, "metadata.json")}
	if configs != nil {
		store.reload = configs.Reload
	}
	return store
}

func (s *GeoIPStore) Status() edgeprotocol.GeoIPStatus {
	dbInfo, dbErr := os.Stat(s.path)
	metaInfo, metaErr := os.Stat(s.metaPath)
	s.statusMu.Lock()
	if s.loaded && sameFileVersion(dbInfo, dbErr, s.dbInfo) && sameFileVersion(metaInfo, metaErr, s.metaInfo) {
		status := s.status
		s.statusMu.Unlock()
		return status
	}
	s.statusMu.Unlock()

	data, err := os.ReadFile(s.metaPath)
	if err != nil {
		return s.cacheStatus(edgeprotocol.GeoIPStatus{}, dbInfo, metaInfo)
	}
	var recorded edgeprotocol.GeoIPStatus
	if json.Unmarshal(data, &recorded) != nil {
		return s.cacheStatus(edgeprotocol.GeoIPStatus{}, dbInfo, metaInfo)
	}
	actual, _, err := geoipdb.Inspect(s.path)
	if err != nil || actual.SHA256 != recorded.SHA256 || actual.Size != recorded.Size || actual.BuildEpoch != recorded.BuildEpoch {
		return s.cacheStatus(edgeprotocol.GeoIPStatus{}, dbInfo, metaInfo)
	}
	return s.cacheStatus(recorded, dbInfo, metaInfo)
}

func (s *GeoIPStore) Install(ctx context.Context, client edgeprotocol.ManagementClient, nodeID string, expected edgeprotocol.GeoIPSyncPayload) error {
	s.installMu.Lock()
	defer s.installMu.Unlock()
	decodedHash, hashErr := hex.DecodeString(expected.SHA256)
	if hashErr != nil || len(decodedHash) != 32 || expected.Size <= 0 || expected.Size > maxGeoIPDatabaseSize {
		return errors.New("invalid GeoIP sync metadata")
	}
	if s.Status() == expected {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.dir, ".GeoLite2-City.mmdb-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { temporary.Close(); os.Remove(temporaryPath) }()
	stream, err := client.DownloadGeoIP(ctx, &edgeprotocol.GeoIPDownloadRequest{NodeID: nodeID, SHA256: expected.SHA256})
	if err != nil {
		return err
	}
	hash := sha256.New()
	var offset int64
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return fmt.Errorf("download GeoIP database: %w", recvErr)
		}
		if chunk.Offset != offset || len(chunk.Data) == 0 {
			return errors.New("invalid GeoIP chunk sequence")
		}
		offset += int64(len(chunk.Data))
		if offset > expected.Size || offset > maxGeoIPDatabaseSize {
			return errors.New("GeoIP database exceeds declared size")
		}
		if _, err = temporary.Write(chunk.Data); err != nil {
			return err
		}
		_, _ = hash.Write(chunk.Data)
	}
	if offset != expected.Size {
		return fmt.Errorf("GeoIP size mismatch: got %d, want %d", offset, expected.Size)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected.SHA256 {
		return errors.New("GeoIP checksum mismatch")
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	buildEpoch, err := validateCityDatabase(temporaryPath)
	if err != nil {
		return err
	}
	if buildEpoch != expected.BuildEpoch {
		return errors.New("GeoIP build epoch mismatch")
	}

	oldMeta, metaErr := os.ReadFile(s.metaPath)
	if metaErr != nil && !errors.Is(metaErr, os.ErrNotExist) {
		return fmt.Errorf("read existing GeoIP metadata: %w", metaErr)
	}
	hadMeta := metaErr == nil
	backup := s.path + ".previous"
	hadOld := false
	if _, statErr := os.Stat(s.path); statErr == nil {
		if err = os.Rename(s.path, backup); err != nil {
			return err
		}
		hadOld = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat existing GeoIP database: %w", statErr)
	}
	restore := func() {
		_ = os.Remove(s.path)
		if hadOld {
			_ = os.Rename(backup, s.path)
		}
		if hadMeta {
			_ = writeAtomicFile(s.metaPath, oldMeta, 0600)
		} else {
			_ = os.Remove(s.metaPath)
		}
		s.invalidateStatus()
		_ = syncDirectory(s.dir)
	}
	if err = os.Rename(temporaryPath, s.path); err != nil {
		restore()
		return err
	}
	metadata, _ := json.Marshal(expected)
	if err = writeAtomicFile(s.metaPath, metadata, 0600); err != nil {
		restore()
		return err
	}
	if err = syncDirectory(s.dir); err != nil {
		restore()
		return err
	}
	if s.reload != nil {
		err = s.reload()
	}
	if err != nil {
		restore()
		if s.reload != nil {
			_ = s.reload()
		}
		return fmt.Errorf("reload Caddy after GeoIP update: %w", err)
	}
	_ = os.Remove(backup)
	_ = syncDirectory(s.dir)
	installedDBInfo, _ := os.Stat(s.path)
	installedMetaInfo, _ := os.Stat(s.metaPath)
	s.cacheStatus(expected, installedDBInfo, installedMetaInfo)
	return nil
}

func validateCityDatabase(path string) (uint64, error) {
	metadata, _, err := geoipdb.Inspect(path)
	if err != nil {
		return 0, err
	}
	return metadata.BuildEpoch, nil
}

func sameFileVersion(current os.FileInfo, currentErr error, cached os.FileInfo) bool {
	if currentErr != nil || current == nil || cached == nil {
		return false
	}
	return os.SameFile(current, cached) && current.Size() == cached.Size() && current.ModTime().Equal(cached.ModTime())
}

func (s *GeoIPStore) cacheStatus(status edgeprotocol.GeoIPStatus, dbInfo, metaInfo os.FileInfo) edgeprotocol.GeoIPStatus {
	s.statusMu.Lock()
	s.loaded, s.status, s.dbInfo, s.metaInfo = true, status, dbInfo, metaInfo
	s.statusMu.Unlock()
	return status
}

func (s *GeoIPStore) invalidateStatus() {
	s.statusMu.Lock()
	s.loaded, s.status, s.dbInfo, s.metaInfo = false, edgeprotocol.GeoIPStatus{}, nil, nil
	s.statusMu.Unlock()
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".metadata-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { temporary.Close(); os.Remove(name) }()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
