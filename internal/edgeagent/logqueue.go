package edgeagent

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
	"goveto-edge/internal/edgeprotocol"
)

var logsBucket = []byte("logs")
var metaBucket = []byte("meta")
var totalBytesKey = []byte("total_bytes")

type LogRecord = edgeprotocol.LogRecord

type LogQueue struct {
	db       *bolt.DB
	notify   chan struct{}
	maxBytes uint64
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
	if len(record.Payload) == 0 {
		return 0, errors.New("log payload is empty")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	var id uint64
	err := q.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(logsBucket)
		sequence, err := bucket.NextSequence()
		if err != nil {
			return err
		}
		id = sequence
		record.ID = id
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		meta := tx.Bucket(metaBucket)
		total := readUint64(meta.Get(totalBytesKey))
		cursor := bucket.Cursor()
		for total+uint64(len(data)) > q.maxBytes {
			key, value := cursor.First()
			if key == nil {
				break
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			if uint64(len(value)) <= total {
				total -= uint64(len(value))
			} else {
				total = 0
			}
		}
		if err := bucket.Put(uint64Key(id), data); err != nil {
			return err
		}
		total += uint64(len(data))
		return meta.Put(totalBytesKey, uint64Key(total))
	})
	if err == nil {
		select {
		case q.notify <- struct{}{}:
		default:
		}
	}
	return id, err
}

func (q *LogQueue) Batch(limit int) ([]LogRecord, error) {
	if limit <= 0 {
		limit = 1000
	}
	result := make([]LogRecord, 0, limit)
	err := q.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(logsBucket).Cursor()
		for key, value := cursor.First(); key != nil && len(result) < limit; key, value = cursor.Next() {
			var record LogRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			result = append(result, record)
		}
		return nil
	})
	return result, err
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
func (q *LogQueue) Close() error          { return q.db.Close() }
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
