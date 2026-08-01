package edgeagent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
	"goveto-edge/internal/edgeprotocol"
)

var logsBucket = []byte("logs")
var metaBucket = []byte("meta")
var totalBytesKey = []byte("total_bytes")
var totalRecordsKey = []byte("total_records")
var droppedRecordsKey = []byte("dropped_records")
var queueVersionKey = []byte("format_version")

const (
	logEnvelopeVersion byte = 1
	logQueueVersion    byte = 2
	logChunkVersion    byte = 1
)

var ErrLogRecordTooLarge = errors.New("log record exceeds queue capacity")

type LogRecord = edgeprotocol.LogRecord

type queuedAccessRecord struct {
	record    LogRecord
	sanitized bool
}

type LogQueue struct {
	db       *bolt.DB
	notify   chan struct{}
	maxBytes uint64

	accessMu              sync.Mutex
	accessBuffer          []queuedAccessRecord
	accessHead            int
	accessCount           int
	accessBufferBytes     uint64
	accessConfig          AccessLogConfig
	accessPolicy          LogPolicy
	accessSignal          chan struct{}
	accessShutdown        chan struct{}
	accessDone            chan struct{}
	accessCancel          context.CancelFunc
	accessShutdownOnce    sync.Once
	accessStarted         bool
	accessAccepting       bool
	accessMemoryDropped   uint64
	accessBatches         uint64
	accessRecords         uint64
	accessLastError       string
	accessLastSuccess     time.Time
	accessPersistOverride func([]LogRecord) ([]uint64, error)
}

type LogQueueStats struct {
	MaxBytes             uint64
	Bytes                uint64
	Records              uint64
	DroppedRecords       uint64
	DiskDroppedRecords   uint64
	MemoryBufferBytes    uint64
	MemoryBufferRecords  uint64
	MemoryDroppedRecords uint64
	CommittedBatches     uint64
	CommittedRecords     uint64
	AverageBatchSize     float64
	LastPersistError     string
	LastPersistSuccess   time.Time
	OldestID             uint64
	NewestID             uint64
}

func OpenLogQueue(path string, maxBytes ...uint64) (*LogQueue, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{
		Timeout: time.Second, NoFreelistSync: true, NoSync: true,
	})
	if err != nil {
		return nil, err
	}
	limit := uint64(2 << 30)
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = maxBytes[0]
	}
	queue := &LogQueue{db: db, notify: make(chan struct{}, 1), maxBytes: limit}
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(logsBucket)
		if err != nil {
			return err
		}
		meta, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		version := meta.Get(queueVersionKey)
		if len(version) != 1 || version[0] != logQueueVersion {
			if bucket.Stats().KeyN > 0 {
				if err = tx.DeleteBucket(logsBucket); err != nil {
					return err
				}
				if _, err = tx.CreateBucket(logsBucket); err != nil {
					return err
				}
			}
			if err = meta.Delete(totalBytesKey); err != nil {
				return err
			}
			if err = meta.Delete(totalRecordsKey); err != nil {
				return err
			}
			if err = meta.Delete(droppedRecordsKey); err != nil {
				return err
			}
		}
		return meta.Put(queueVersionKey, []byte{logQueueVersion})
	}); err != nil {
		db.Close()
		return nil, err
	}
	return queue, nil
}

func (q *LogQueue) Append(record LogRecord) (uint64, error) {
	ids, err := q.AppendBatch([]LogRecord{record})
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// AppendBatch commits all records in one transaction and emits one wakeup.
// BatchSized syncs committed pages before any record can leave the agent.
func (q *LogQueue) AppendBatch(records []LogRecord) ([]uint64, error) {
	if len(records) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	chunks, err := buildLogChunks(records, now, q.maxBytes)
	if err != nil {
		return nil, err
	}

	ids := make([]uint64, len(records))
	err = q.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(logsBucket)
		meta := tx.Bucket(metaBucket)
		firstID := bucket.Sequence() + 1
		if err := bucket.SetSequence(firstID + uint64(len(records)) - 1); err != nil {
			return err
		}

		total := readUint64(meta.Get(totalBytesKey))
		totalRecords := readUint64(meta.Get(totalRecordsKey))
		dropped := readUint64(meta.Get(droppedRecordsKey))
		for index := range ids {
			ids[index] = firstID + uint64(index)
		}
		for _, chunk := range chunks {
			cursor := bucket.Cursor()
			for total+uint64(len(chunk.data)) > q.maxBytes {
				key, value := cursor.First()
				if key == nil {
					break
				}
				count, countErr := logChunkCount(value)
				if countErr != nil {
					return countErr
				}
				if err := cursor.Delete(); err != nil {
					return err
				}
				dropped += uint64(count)
				totalRecords -= min(totalRecords, uint64(count))
				if uint64(len(value)) <= total {
					total -= uint64(len(value))
				} else {
					total = 0
				}
			}
			var key [8]byte
			binary.BigEndian.PutUint64(key[:], firstID+uint64(chunk.start))
			if err := bucket.Put(key[:], chunk.data); err != nil {
				return err
			}
			total += uint64(len(chunk.data))
			totalRecords += uint64(chunk.count)
		}
		if err := putUint64(meta, droppedRecordsKey, dropped); err != nil {
			return err
		}
		if err := putUint64(meta, totalBytesKey, total); err != nil {
			return err
		}
		return putUint64(meta, totalRecordsKey, totalRecords)
	})
	if err != nil {
		return nil, err
	}
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return ids, nil
}

func (q *LogQueue) Batch(limit int) ([]LogRecord, error) {
	result, _, err := q.BatchSized(limit, 0)
	return result, err
}

func (q *LogQueue) BatchSized(limit int, maxBytes uint64) ([]LogRecord, uint64, error) {
	if limit <= 0 {
		limit = 1000
	}
	if err := q.db.Sync(); err != nil {
		return nil, 0, err
	}
	result := make([]LogRecord, 0, limit)
	var size uint64
	err := q.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(logsBucket).Cursor()
		for key, value := cursor.First(); key != nil && len(result) < limit; key, value = cursor.Next() {
			firstID := binary.BigEndian.Uint64(key)
			var chunkErr error
			stopped := false
			visitErr := visitLogChunk(value, func(index int, envelope []byte) bool {
				recordBytes := uint64(len(envelope) + 4)
				if len(result) >= limit || maxBytes > 0 && size+recordBytes > maxBytes {
					stopped = true
					return false
				}
				record, decodeErr := decodeLogEnvelope(envelope, firstID+uint64(index))
				if decodeErr != nil {
					chunkErr = decodeErr
					return false
				}
				result = append(result, record)
				size += recordBytes
				return true
			})
			if visitErr != nil {
				return visitErr
			}
			if chunkErr != nil {
				return chunkErr
			}
			if stopped || len(result) >= limit {
				break
			}
		}
		return nil
	})
	return result, size, err
}

func (q *LogQueue) DropOversizedHead(maxBytes uint64) (uint64, error) {
	if maxBytes == 0 {
		return 0, nil
	}
	var dropped uint64
	err := q.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(logsBucket)
		meta := tx.Bucket(metaBucket)
		total := readUint64(meta.Get(totalBytesKey))
		totalRecords := readUint64(meta.Get(totalRecordsKey))
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.First() {
			oversized, err := oversizedLogChunkHead(value, maxBytes)
			if err != nil {
				return err
			}
			if oversized == 0 {
				break
			}
			firstID := binary.BigEndian.Uint64(key)
			trimmed, err := trimLogChunk(value, oversized)
			if err != nil {
				return err
			}
			if err = cursor.Delete(); err != nil {
				return err
			}
			total -= min(total, uint64(len(value)))
			totalRecords -= min(totalRecords, uint64(oversized))
			dropped += uint64(oversized)
			if len(trimmed) > 0 {
				var nextKey [8]byte
				binary.BigEndian.PutUint64(nextKey[:], firstID+uint64(oversized))
				if err = bucket.Put(nextKey[:], trimmed); err != nil {
					return err
				}
				total += uint64(len(trimmed))
			}
		}
		if dropped == 0 {
			return nil
		}
		if err := putUint64(meta, totalBytesKey, total); err != nil {
			return err
		}
		if err := putUint64(meta, totalRecordsKey, totalRecords); err != nil {
			return err
		}
		return putUint64(meta, droppedRecordsKey, readUint64(meta.Get(droppedRecordsKey))+dropped)
	})
	return dropped, err
}

func (q *LogQueue) Ack(through uint64) error {
	return q.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(logsBucket)
		meta := tx.Bucket(metaBucket)
		total := readUint64(meta.Get(totalBytesKey))
		totalRecords := readUint64(meta.Get(totalRecordsKey))
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.First() {
			firstID := binary.BigEndian.Uint64(key)
			if firstID > through {
				break
			}
			count, err := logChunkCount(value)
			if err != nil {
				return err
			}
			remove := min(count, int(through-firstID+1))
			trimmed, err := trimLogChunk(value, remove)
			if err != nil {
				return err
			}
			if err = cursor.Delete(); err != nil {
				return err
			}
			total -= min(total, uint64(len(value)))
			totalRecords -= min(totalRecords, uint64(remove))
			if len(trimmed) > 0 {
				var nextKey [8]byte
				binary.BigEndian.PutUint64(nextKey[:], firstID+uint64(remove))
				if err = bucket.Put(nextKey[:], trimmed); err != nil {
					return err
				}
				total += uint64(len(trimmed))
				break
			}
		}
		if err := putUint64(meta, totalBytesKey, total); err != nil {
			return err
		}
		return putUint64(meta, totalRecordsKey, totalRecords)
	})
}
func (q *LogQueue) Wait() <-chan struct{} { return q.notify }

func (q *LogQueue) Close() error {
	var flushErr error
	q.accessMu.Lock()
	started := q.accessStarted
	q.accessMu.Unlock()
	if started {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		flushErr = q.ShutdownAccess(ctx)
		cancel()
	}
	return errors.Join(flushErr, q.db.Sync(), q.db.Close())
}

func (q *LogQueue) Stats() (LogQueueStats, error) {
	stats := LogQueueStats{MaxBytes: q.maxBytes}
	err := q.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(logsBucket)
		meta := tx.Bucket(metaBucket)
		stats.Bytes = readUint64(meta.Get(totalBytesKey))
		stats.DiskDroppedRecords = readUint64(meta.Get(droppedRecordsKey))
		stats.Records = readUint64(meta.Get(totalRecordsKey))
		first, _ := bucket.Cursor().First()
		last, lastValue := bucket.Cursor().Last()
		if first != nil {
			stats.OldestID = binary.BigEndian.Uint64(first)
		}
		if last != nil {
			count, err := logChunkCount(lastValue)
			if err != nil {
				return err
			}
			stats.NewestID = binary.BigEndian.Uint64(last) + uint64(count) - 1
		}
		return nil
	})
	q.accessMu.Lock()
	stats.MemoryBufferBytes = q.accessBufferBytes
	stats.MemoryBufferRecords = uint64(q.accessCount)
	stats.MemoryDroppedRecords = q.accessMemoryDropped
	stats.CommittedBatches = q.accessBatches
	stats.CommittedRecords = q.accessRecords
	if q.accessBatches > 0 {
		stats.AverageBatchSize = float64(q.accessRecords) / float64(q.accessBatches)
	}
	stats.LastPersistError = q.accessLastError
	stats.LastPersistSuccess = q.accessLastSuccess
	q.accessMu.Unlock()
	stats.DroppedRecords = stats.DiskDroppedRecords + stats.MemoryDroppedRecords
	return stats, err
}
func putUint64(bucket *bolt.Bucket, key []byte, value uint64) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return bucket.Put(key, encoded[:])
}
func readUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}

type encodedLogChunk struct {
	start int
	count int
	data  []byte
}

func buildLogChunks(records []LogRecord, defaultTime time.Time, maxBytes uint64) ([]encodedLogChunk, error) {
	const maxChunkRecords = 1024
	chunks := make([]encodedLogChunk, 0, (len(records)+maxChunkRecords-1)/maxChunkRecords)
	for start := 0; start < len(records); {
		size := 5
		end := start
		for end < len(records) && end-start < maxChunkRecords {
			record := records[end]
			if len(record.Payload) == 0 {
				return nil, fmt.Errorf("log record %d: log payload is empty", end)
			}
			if !json.Valid(record.Payload) {
				return nil, fmt.Errorf("log record %d: log payload is invalid JSON", end)
			}
			recordSize, err := logEnvelopeSize(record)
			if err != nil {
				return nil, fmt.Errorf("log record %d: %w", end, err)
			}
			if uint64(5+4+recordSize) > maxBytes {
				return nil, ErrLogRecordTooLarge
			}
			if end > start && uint64(size+4+recordSize) > maxBytes {
				break
			}
			size += 4 + recordSize
			end++
		}
		data := make([]byte, size)
		data[0] = logChunkVersion
		binary.BigEndian.PutUint32(data[1:5], uint32(end-start))
		offset := 5
		for index := start; index < end; index++ {
			recordSize, _ := logEnvelopeSize(records[index])
			binary.BigEndian.PutUint32(data[offset:offset+4], uint32(recordSize))
			offset += 4
			encodeLogEnvelopeInto(data[offset:offset+recordSize], records[index], defaultTime)
			offset += recordSize
		}
		chunks = append(chunks, encodedLogChunk{start: start, count: end - start, data: data})
		start = end
	}
	return chunks, nil
}

func logChunkCount(data []byte) (int, error) {
	if len(data) < 5 || data[0] != logChunkVersion {
		return 0, errors.New("unsupported log queue chunk format")
	}
	count := int(binary.BigEndian.Uint32(data[1:5]))
	if count < 1 {
		return 0, errors.New("empty log queue chunk")
	}
	return count, nil
}

func visitLogChunk(data []byte, visit func(index int, envelope []byte) bool) error {
	count, err := logChunkCount(data)
	if err != nil {
		return err
	}
	offset := 5
	for index := 0; index < count; index++ {
		if offset+4 > len(data) {
			return errors.New("truncated log queue chunk")
		}
		size := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		offset += 4
		if size < 1 || offset+size > len(data) {
			return errors.New("invalid log queue chunk record length")
		}
		if !visit(index, data[offset:offset+size]) {
			return nil
		}
		offset += size
	}
	if offset != len(data) {
		return errors.New("log queue chunk has trailing data")
	}
	return nil
}

func trimLogChunk(data []byte, remove int) ([]byte, error) {
	count, err := logChunkCount(data)
	if err != nil {
		return nil, err
	}
	if remove < 0 || remove > count {
		return nil, errors.New("invalid log queue chunk trim")
	}
	if remove == count {
		return nil, nil
	}
	offset := 5
	for range remove {
		if offset+4 > len(data) {
			return nil, errors.New("truncated log queue chunk")
		}
		size := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if size < 1 || offset+4+size > len(data) {
			return nil, errors.New("invalid log queue chunk record length")
		}
		offset += 4 + size
	}
	trimmed := make([]byte, 5+len(data)-offset)
	trimmed[0] = logChunkVersion
	binary.BigEndian.PutUint32(trimmed[1:5], uint32(count-remove))
	copy(trimmed[5:], data[offset:])
	return trimmed, nil
}

func oversizedLogChunkHead(data []byte, maxBytes uint64) (int, error) {
	oversized := 0
	err := visitLogChunk(data, func(_ int, envelope []byte) bool {
		if uint64(len(envelope)+4) <= maxBytes {
			return false
		}
		oversized++
		return true
	})
	return oversized, err
}

func encodeLogEnvelope(record LogRecord, defaultTime time.Time) ([]byte, error) {
	size, err := logEnvelopeSize(record)
	if err != nil {
		return nil, err
	}
	data := make([]byte, size)
	encodeLogEnvelopeInto(data, record, defaultTime)
	return data, nil
}

func logEnvelopeSize(record LogRecord) (int, error) {
	if len(record.Type) > 1<<16-1 || len(record.SiteID) > 1<<16-1 || uint64(len(record.Payload)) > 1<<32-1 {
		return 0, errors.New("log record metadata is too long")
	}
	return 1 + 2 + 2 + 8 + 8 + 4 + len(record.Type) + len(record.SiteID) + len(record.Payload), nil
}

func encodeLogEnvelopeInto(data []byte, record LogRecord, defaultTime time.Time) {
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = defaultTime
	}
	const headerSize = 1 + 2 + 2 + 8 + 8 + 4
	data[0] = logEnvelopeVersion
	binary.BigEndian.PutUint16(data[1:3], uint16(len(record.Type)))
	binary.BigEndian.PutUint16(data[3:5], uint16(len(record.SiteID)))
	binary.BigEndian.PutUint64(data[5:13], record.ConfigVersion)
	binary.BigEndian.PutUint64(data[13:21], uint64(createdAt.UnixNano()))
	binary.BigEndian.PutUint32(data[21:25], uint32(len(record.Payload)))
	offset := headerSize
	offset += copy(data[offset:], record.Type)
	offset += copy(data[offset:], record.SiteID)
	copy(data[offset:], record.Payload)
}

func decodeLogEnvelope(data []byte, id uint64) (LogRecord, error) {
	const headerSize = 1 + 2 + 2 + 8 + 8 + 4
	if len(data) < headerSize || data[0] != logEnvelopeVersion {
		return LogRecord{}, errors.New("unsupported log queue record format")
	}
	typeSize := int(binary.BigEndian.Uint16(data[1:3]))
	siteSize := int(binary.BigEndian.Uint16(data[3:5]))
	payloadSize := int(binary.BigEndian.Uint32(data[21:25]))
	if headerSize+typeSize+siteSize+payloadSize != len(data) {
		return LogRecord{}, errors.New("invalid log queue record length")
	}
	offset := headerSize
	record := LogRecord{
		ID: id, Type: string(data[offset : offset+typeSize]),
		ConfigVersion: binary.BigEndian.Uint64(data[5:13]),
		CreatedAt:     time.Unix(0, int64(binary.BigEndian.Uint64(data[13:21]))).UTC(),
	}
	offset += typeSize
	record.SiteID = string(data[offset : offset+siteSize])
	offset += siteSize
	record.Payload = append(json.RawMessage(nil), data[offset:]...)
	if !json.Valid(record.Payload) {
		return LogRecord{}, errors.New("invalid log queue JSON payload")
	}
	return record, nil
}
