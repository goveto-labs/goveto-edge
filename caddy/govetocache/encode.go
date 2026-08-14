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
// wait on the encoder. Tests may set it to 0 to force a drop.
var encodeQueueSlots = 4

type encodeSession struct {
	mu      sync.Mutex
	chunks  chan []byte
	done    chan error
	dropped bool
	closed  bool
}

func startEncodeSession(put func(io.Reader) error, headerBytes []byte) *encodeSession {
	chunks := make(chan []byte, encodeQueueSlots)
	pr, pw := io.Pipe()
	sess := &encodeSession{chunks: chunks, done: make(chan error, 1)}
	go func() {
		defer pw.Close()
		for chunk := range chunks {
			if _, err := pw.Write(chunk); err != nil {
				return
			}
		}
	}()
	go func() {
		err := put(io.MultiReader(bytes.NewReader(headerBytes), pr))
		_ = pr.Close()
		sess.done <- err
	}()
	return sess
}

func (s *encodeSession) tryWrite(p []byte) {
	if s == nil || len(p) == 0 {
		return
	}
	cp := bytes.Clone(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped || s.closed {
		return
	}
	select {
	case s.chunks <- cp:
	default:
		s.dropped = true
		s.closed = true
		close(s.chunks)
	}
}

func (s *encodeSession) finish() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.chunks)
	}
	dropped := s.dropped
	s.mu.Unlock()
	err := <-s.done
	if dropped {
		return errEncodeDropped
	}
	return err
}

func (s *encodeSession) abandon() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.dropped = true
	if !s.closed {
		s.closed = true
		close(s.chunks)
	}
	s.mu.Unlock()
	<-s.done
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
	}, headerBytes)
}

func (w *capturedResponse) finishEncode() error {
	if w.encode == nil {
		return nil
	}
	enc := w.encode
	w.encode = nil
	return enc.finish()
}

func (w *capturedResponse) abandonEncode() {
	if w.encode == nil {
		return
	}
	enc := w.encode
	w.encode = nil
	enc.abandon()
}
