// Package geoipdb validates node-managed MaxMind City databases.
package geoipdb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/oschwald/maxminddb-golang"
)

const MaxSize int64 = 256 << 20

type Metadata struct {
	SHA256     string
	Size       int64
	BuildEpoch uint64
}

// Inspect reads and validates one immutable snapshot of path. The returned
// bytes are the exact bytes used for metadata and checksum validation.
func Inspect(path string) (Metadata, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Metadata{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return Metadata{}, nil, errors.New("GeoIP database must be a regular file")
	}
	if info.Size() <= 0 || info.Size() > MaxSize {
		return Metadata{}, nil, fmt.Errorf("GeoIP database size %d is outside the supported range", info.Size())
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxSize+1))
	if err != nil {
		return Metadata{}, nil, err
	}
	if int64(len(data)) != info.Size() {
		return Metadata{}, nil, errors.New("GeoIP database changed while it was being read")
	}
	reader, err := maxminddb.FromBytes(data)
	if err != nil {
		return Metadata{}, nil, fmt.Errorf("open GeoIP database: %w", err)
	}
	defer reader.Close()
	if !strings.HasSuffix(strings.ToLower(reader.Metadata.DatabaseType), "-city") {
		return Metadata{}, nil, fmt.Errorf("GeoIP database type %q is not a City database", reader.Metadata.DatabaseType)
	}
	hash := sha256.Sum256(data)
	return Metadata{
		SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data)), BuildEpoch: uint64(reader.Metadata.BuildEpoch),
	}, data, nil
}
