package govetocache

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var errEncodeDropped = errors.New("cache encode dropped")

// encodeQueueSlots is the bounded number of in-flight origin chunks that may
// wait on the encoder. For the ~32 KiB writes produced by Caddy's reverse proxy,
// this is roughly 2 MiB per session; other handlers may use larger writes.
// Tests may set it to 0 to force a drop on every write.
var encodeQueueSlots = 64

// encodeCleanupTimeout bounds synchronous cleanup after a session is
// abandoned. Successful commits are not subject to this timeout.
var encodeCleanupTimeout = 2 * time.Second

type encodeSession struct {
	mu            sync.Mutex
	sendMu        sync.Mutex
	dropOnce      sync.Once
	queueDropOnce sync.Once
	chunks        chan []byte
	stop          chan struct{}
	pipe          *io.PipeWriter
	onQueueFull   func()
	dropped       bool
	closed        bool
	result        error
	done          chan struct{}
}

func startEncodeSession(put func(io.Reader) error, headerBytes []byte, onQueueFull func()) *encodeSession {
	sess := &encodeSession{
		chunks:      make(chan []byte, encodeQueueSlots),
		stop:        make(chan struct{}),
		onQueueFull: onQueueFull,
		done:        make(chan struct{}),
	}
	pr, pw := io.Pipe()
	sess.pipe = pw
	go func() {
		defer pw.Close()
		for {
			select {
			case chunk, ok := <-sess.chunks:
				if !ok {
					return
				}
				if _, err := pw.Write(chunk); err != nil {
					return
				}
			case <-sess.stop:
				return
			}
		}
	}()
	go func() {
		err := put(io.MultiReader(bytes.NewReader(headerBytes), pr))
		_ = pr.Close()
		sess.mu.Lock()
		sess.result = err
		sess.mu.Unlock()
		close(sess.done)
	}()
	return sess
}

func (s *encodeSession) tryWrite(p []byte) {
	if s == nil || len(p) == 0 {
		return
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	if s.dropped || s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if cap(s.chunks) == 0 {
		s.dropQueueFull()
		return
	}
	cp := bytes.Clone(p)
	select {
	case s.chunks <- cp:
	case <-s.stop:
	default:
		s.dropQueueFull()
	}
}

func (s *encodeSession) dropQueueFull() {
	s.queueDropOnce.Do(func() {
		if s.onQueueFull != nil {
			s.onQueueFull()
		}
	})
	s.drop()
}

// drop aborts the session and closes the pipe from outside the drainer so a
// blocked pipe write is interrupted instead of publishing a truncated object.
func (s *encodeSession) drop() {
	s.dropOnce.Do(func() {
		s.mu.Lock()
		s.dropped = true
		s.mu.Unlock()
		_ = s.pipe.CloseWithError(errEncodeDropped)
		close(s.stop)
	})
}

func (s *encodeSession) finish() error {
	if s == nil {
		return nil
	}
	s.sendMu.Lock()
	s.mu.Lock()
	if s.dropped {
		s.mu.Unlock()
		s.sendMu.Unlock()
		return errEncodeDropped
	}
	if !s.closed {
		s.closed = true
		close(s.chunks)
	}
	s.mu.Unlock()
	s.sendMu.Unlock()

	<-s.done
	s.mu.Lock()
	dropped := s.dropped
	err := s.result
	s.mu.Unlock()
	if dropped {
		return errEncodeDropped
	}
	return err
}

func (s *encodeSession) abandon() {
	if s == nil {
		return
	}
	s.drop()
	timer := time.NewTimer(encodeCleanupTimeout)
	defer timer.Stop()
	select {
	case <-s.done:
	case <-timer.C:
	}
}

func hasDeclaredTrailer(header http.Header) bool {
	if header.Get("Trailer") != "" {
		return true
	}
	for name := range header {
		if strings.HasPrefix(name, http.TrailerPrefix) {
			return true
		}
	}
	return false
}

func (w *capturedResponse) startEncode(h *Handler, request *http.Request, header http.Header, status int, size uint64, baseKey string, varied http.Header) {
	if h.storage == nil {
		return
	}
	headerBytes, err := serializedHeader(status, h.cacheStorageHeader(header), int64(size), request.Method)
	if err != nil {
		return
	}
	variedRaw := h.cacheKey(request, varied)
	variedKey := h.storageKey(variedRaw)
	ttl := time.Duration(h.ttl(status)) * time.Second
	groups := surrogateGroups(header, h.SurrogateKeyHeader)
	etag := header.Get("ETag")
	realKey := purgeKey(request)
	logical := uint64(len(headerBytes)) + size
	w.encode = startEncodeSession(func(source io.Reader) error {
		return h.storage.PutReader(baseKey, variedKey, source, logical, groups, varied, etag, ttl, realKey)
	}, headerBytes, h.storage.RecordStreamEncodeDrop)
}

func (w *capturedResponse) finishEncode() error {
	if w.encode == nil {
		return nil
	}
	enc := w.encode
	w.encode = nil
	err := enc.finish()
	if errors.Is(err, errEncodeDropped) {
		enc.abandon()
	}
	return err
}

func (w *capturedResponse) abandonEncode() {
	if w.encode == nil {
		return
	}
	enc := w.encode
	w.encode = nil
	enc.abandon()
}
