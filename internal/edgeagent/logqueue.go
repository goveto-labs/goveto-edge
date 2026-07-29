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
var droppedRecordsKey = []byte("dropped_records")

var ErrLogRecordTooLarge = errors.New("log record exceeds queue capacity")

type LogRecord = edgeprotocol.LogRecord

type LogQueue struct {
	db       *bolt.DB
	notify   chan struct{}
	maxBytes uint64

	accessMu              sync.Mutex
	accessBuffer          []LogRecord
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
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, NoFreelistSync: true})
	if err != nil {
		return nil, err
	}
	limit := uint64(2 << 30)
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = maxBytes[0]
	}
	queue := &LogQueue{db: db, notify: make(chan struct{}, 1), maxBytes: limit}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(logsBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(metaBucket)
		return err
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

// AppendBatch durably commits all records in one transaction and emits one wakeup.
func (q *LogQueue) AppendBatch(records []LogRecord) ([]uint64, error) {
	if len(records) == 0 {
		return nil, nil
	}
	prepared := append([]LogRecord(nil), records...)
	now := time.Now().UTC()
	for index := range prepared {
		if len(prepared[index].Payload) == 0 {
			return nil, fmt.Errorf("log record %d: log payload is empty", index)
		}
		if prepared[index].CreatedAt.IsZero() {
			prepared[index].CreatedAt = now
		}
	}

	ids := make([]uint64, len(prepared))
	err := q.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(logsBucket)
		meta := tx.Bucket(metaBucket)
		firstID := bucket.Sequence() + 1
		encoded := make([][]byte, len(prepared))
		for index := range prepared {
			ids[index] = firstID + uint64(index)
			prepared[index].ID = ids[index]
			data, err := json.Marshal(prepared[index])
			if err != nil {
				return fmt.Errorf("marshal log record %d: %w", index, err)
			}
			if uint64(len(data)) > q.maxBytes {
				return ErrLogRecordTooLarge
			}
			encoded[index] = data
		}
		if err := bucket.SetSequence(firstID + uint64(len(prepared)) - 1); err != nil {
			return err
		}

		total := readUint64(meta.Get(totalBytesKey))
		dropped := readUint64(meta.Get(droppedRecordsKey))
		for index, data := range encoded {
			cursor := bucket.Cursor()
			for total+uint64(len(data)) > q.maxBytes {
				key, value := cursor.First()
				if key == nil {
					break
				}
				if err := cursor.Delete(); err != nil {
					return err
				}
				dropped++
				if uint64(len(value)) <= total {
					total -= uint64(len(value))
				} else {
					total = 0
				}
			}
			if err := bucket.Put(uint64Key(ids[index]), data); err != nil {
				return err
			}
			total += uint64(len(data))
		}
		if err := meta.Put(droppedRecordsKey, uint64Key(dropped)); err != nil {
			return err
		}
		return meta.Put(totalBytesKey, uint64Key(total))
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
	result := make([]LogRecord, 0, limit)
	var size uint64
	err := q.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(logsBucket).Cursor()
		for key, value := cursor.First(); key != nil && len(result) < limit; key, value = cursor.Next() {
			if maxBytes > 0 && size+uint64(len(value)) > maxBytes {
				break
			}
			var record LogRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			result = append(result, record)
			size += uint64(len(value))
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
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil && uint64(len(value)) > maxBytes; key, value = cursor.First() {
			if err := cursor.Delete(); err != nil {
				return err
			}
			dropped++
			if uint64(len(value)) <= total {
				total -= uint64(len(value))
			} else {
				total = 0
			}
		}
		if dropped == 0 {
			return nil
		}
		if err := meta.Put(totalBytesKey, uint64Key(total)); err != nil {
			return err
		}
		return meta.Put(
			droppedRecordsKey,
			uint64Key(readUint64(meta.Get(droppedRecordsKey))+dropped),
		)
	})
	return dropped, err
}

func (q *LogQueue) Ack(through uint64) error {
	return q.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(logsBucket)
		meta := tx.Bucket(metaBucket)
		total := readUint64(meta.Get(totalBytesKey))
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil && binary.BigEndian.Uint64(key) <= through; key, value = cursor.Next() {
			if uint64(len(value)) <= total {
				total -= uint64(len(value))
			} else {
				total = 0
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
		return meta.Put(totalBytesKey, uint64Key(total))
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
	return errors.Join(flushErr, q.db.Close())
}

func (q *LogQueue) Stats() (LogQueueStats, error) {
	stats := LogQueueStats{MaxBytes: q.maxBytes}
	err := q.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(logsBucket)
		meta := tx.Bucket(metaBucket)
		stats.Bytes = readUint64(meta.Get(totalBytesKey))
		stats.DiskDroppedRecords = readUint64(meta.Get(droppedRecordsKey))
		stats.Records = uint64(bucket.Stats().KeyN)
		first, _ := bucket.Cursor().First()
		last, _ := bucket.Cursor().Last()
		if first != nil {
			stats.OldestID = binary.BigEndian.Uint64(first)
		}
		if last != nil {
			stats.NewestID = binary.BigEndian.Uint64(last)
		}
		return nil
	})
	q.accessMu.Lock()
	stats.MemoryBufferBytes = q.accessBufferBytes
	stats.MemoryBufferRecords = uint64(len(q.accessBuffer))
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
func uint64Key(value uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, value)
	return key
}
func readUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
