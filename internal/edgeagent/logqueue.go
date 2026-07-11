package edgeagent

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

var logsBucket = []byte("logs")

type LogRecord struct {
	ID        uint64          `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

type LogQueue struct {
	db     *bolt.DB
	notify chan struct{}
}

func OpenLogQueue(path string) (*LogQueue, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, NoFreelistSync: true})
	if err != nil {
		return nil, err
	}
	queue := &LogQueue{db: db, notify: make(chan struct{}, 1)}
	if err := db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucketIfNotExists(logsBucket); return err }); err != nil {
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
		return bucket.Put(uint64Key(id), data)
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
		cursor := tx.Bucket(logsBucket).Cursor()
		for key, _ := cursor.First(); key != nil && binary.BigEndian.Uint64(key) <= through; key, _ = cursor.Next() {
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
}
func (q *LogQueue) Wait() <-chan struct{} { return q.notify }
func (q *LogQueue) Close() error          { return q.db.Close() }
func uint64Key(value uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, value)
	return key
}
