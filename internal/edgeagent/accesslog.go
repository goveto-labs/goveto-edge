package edgeagent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	defaultAccessLogBufferBytes   = uint64(16 << 20)
	defaultAccessLogBufferRecords = 16_384
	defaultAccessLogBatchBytes    = uint64(1 << 20)
	defaultAccessLogBatchRecords  = 512
	defaultAccessLogFlushInterval = 10 * time.Millisecond
)

type AccessLogConfig struct {
	BufferBytes   uint64
	BufferRecords int
	BatchBytes    uint64
	BatchRecords  int
	FlushInterval time.Duration
}

func defaultAccessLogConfig() AccessLogConfig {
	return AccessLogConfig{
		BufferBytes: defaultAccessLogBufferBytes, BufferRecords: defaultAccessLogBufferRecords,
		BatchBytes: defaultAccessLogBatchBytes, BatchRecords: defaultAccessLogBatchRecords,
		FlushInterval: defaultAccessLogFlushInterval,
	}
}

func normalizeAccessLogConfig(config AccessLogConfig) AccessLogConfig {
	defaults := defaultAccessLogConfig()
	if config.BufferBytes == 0 {
		config.BufferBytes = defaults.BufferBytes
	}
	if config.BufferRecords <= 0 {
		config.BufferRecords = defaults.BufferRecords
	}
	if config.BatchBytes == 0 {
		config.BatchBytes = defaults.BatchBytes
	}
	if config.BatchRecords <= 0 {
		config.BatchRecords = defaults.BatchRecords
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = defaults.FlushInterval
	}
	return config
}

func (q *LogQueue) StartAccessPipeline(policy LogPolicy, config AccessLogConfig) error {
	q.accessMu.Lock()
	defer q.accessMu.Unlock()
	if q.accessStarted {
		return errors.New("access log pipeline already started")
	}
	config = normalizeAccessLogConfig(config)
	ctx, cancel := context.WithCancel(context.Background())
	q.accessConfig = config
	q.accessPolicy = policy
	q.accessBuffer = make([]queuedAccessRecord, config.BufferRecords)
	q.accessHead = 0
	q.accessCount = 0
	q.accessSignal = make(chan struct{}, 1)
	q.accessShutdown = make(chan struct{})
	q.accessDone = make(chan struct{})
	q.accessCancel = cancel
	q.accessStarted = true
	q.accessAccepting = true
	go q.runAccessPipeline(ctx)
	return nil
}

// EnqueueAccess adds an access record to the in-memory queue without waiting for disk.
// The caller must not mutate record.Payload after a successful enqueue.
func (q *LogQueue) EnqueueAccess(record LogRecord) bool {
	return q.enqueueAccess(record, false)
}

func (q *LogQueue) EnqueueSanitizedAccess(record LogRecord) bool {
	return q.enqueueAccess(record, true)
}

func (q *LogQueue) enqueueAccess(record LogRecord, sanitized bool) bool {
	if len(record.Payload) == 0 {
		return false
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	size := uint64(len(record.Payload))
	q.accessMu.Lock()
	if !q.accessAccepting || q.accessCount >= len(q.accessBuffer) || q.accessBufferBytes+size > q.accessConfig.BufferBytes {
		q.accessMemoryDropped++
		q.accessMu.Unlock()
		return false
	}
	position := (q.accessHead + q.accessCount) % len(q.accessBuffer)
	q.accessBuffer[position] = queuedAccessRecord{record: record, sanitized: sanitized}
	q.accessCount++
	q.accessBufferBytes += size
	flush := q.accessCount >= q.accessConfig.BatchRecords || q.accessBufferBytes >= q.accessConfig.BatchBytes
	signal := q.accessSignal
	q.accessMu.Unlock()
	if flush {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	return true
}

func (q *LogQueue) ShutdownAccess(ctx context.Context) error {
	q.accessMu.Lock()
	if !q.accessStarted {
		q.accessMu.Unlock()
		return nil
	}
	q.accessAccepting = false
	shutdown := q.accessShutdown
	done := q.accessDone
	cancelWorker := q.accessCancel
	q.accessShutdownOnce.Do(func() { close(shutdown) })
	q.accessMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		cancelWorker()
		<-done
		q.accessMu.Lock()
		remainingRecords := q.accessCount
		remainingBytes := q.accessBufferBytes
		q.accessMu.Unlock()
		return fmt.Errorf("flush access logs: %w (remaining records=%d bytes=%d)", ctx.Err(), remainingRecords, remainingBytes)
	}
}

func (q *LogQueue) runAccessPipeline(ctx context.Context) {
	ticker := time.NewTicker(q.accessConfig.FlushInterval)
	defer ticker.Stop()
	defer close(q.accessDone)
	shuttingDown := false
	for {
		if !shuttingDown {
			select {
			case <-ctx.Done():
				return
			case <-q.accessShutdown:
				shuttingDown = true
			case <-q.accessSignal:
			case <-ticker.C:
			}
		}

		for {
			records, rawBytes := q.peekAccessBatch()
			if len(records) == 0 {
				if shuttingDown {
					return
				}
				break
			}
			if !q.persistAccessBatch(ctx, records) {
				return
			}
			q.completeAccessBatch(len(records), rawBytes)
			if !shuttingDown && !q.accessBatchReady() {
				break
			}
		}
	}
}

func (q *LogQueue) peekAccessBatch() ([]queuedAccessRecord, uint64) {
	q.accessMu.Lock()
	defer q.accessMu.Unlock()
	limit := min(q.accessCount, q.accessConfig.BatchRecords, len(q.accessBuffer)-q.accessHead)
	var bytes uint64
	for index := 0; index < limit; index++ {
		size := uint64(len(q.accessBuffer[q.accessHead+index].record.Payload))
		if index > 0 && bytes+size > q.accessConfig.BatchBytes {
			limit = index
			break
		}
		bytes += size
	}
	return q.accessBuffer[q.accessHead : q.accessHead+limit], bytes
}

func (q *LogQueue) accessBatchReady() bool {
	q.accessMu.Lock()
	defer q.accessMu.Unlock()
	return q.accessCount >= q.accessConfig.BatchRecords || q.accessBufferBytes >= q.accessConfig.BatchBytes
}

func (q *LogQueue) persistAccessBatch(ctx context.Context, raw []queuedAccessRecord) bool {
	processed := make([]LogRecord, 0, len(raw))
	for _, queued := range raw {
		record := queued.record
		payload, keep := record.Payload, false
		if queued.sanitized {
			keep = q.accessPolicy.Keep(record.Payload)
		} else {
			payload, keep = q.accessPolicy.Apply(record.Payload)
		}
		if !keep {
			continue
		}
		record.Payload = payload
		processed = append(processed, record)
	}
	if len(processed) == 0 {
		return true
	}

	backoff := 10 * time.Millisecond
	for {
		q.accessMu.Lock()
		persist := q.accessPersistOverride
		q.accessMu.Unlock()
		if persist == nil {
			persist = q.AppendBatch
		}
		ids, err := persist(processed)
		if err == nil {
			q.accessMu.Lock()
			q.accessBatches++
			q.accessRecords += uint64(len(ids))
			q.accessLastSuccess = time.Now().UTC()
			q.accessMu.Unlock()
			return true
		}
		q.accessMu.Lock()
		q.accessLastError = err.Error()
		q.accessMu.Unlock()
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false
		case <-timer.C:
		}
		backoff = min(backoff*2, time.Second)
	}
}

func (q *LogQueue) completeAccessBatch(records int, bytes uint64) {
	q.accessMu.Lock()
	defer q.accessMu.Unlock()
	clear(q.accessBuffer[q.accessHead : q.accessHead+records])
	q.accessHead = (q.accessHead + records) % len(q.accessBuffer)
	q.accessCount -= records
	q.accessBufferBytes -= min(bytes, q.accessBufferBytes)
}
