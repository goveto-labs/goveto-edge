package govetocache

import (
	"errors"
	"io"
	"net/http"
	"os"

	"goveto-edge/internal/httpconditional"
)

const defaultCaptureLimit = 64 << 20

// captureSlots bounds concurrent origin captures process-wide on purpose: the
// limit protects the machine's memory/disk headroom, so it must be shared by
// every Handler instance and site rather than scoped per handler.
var captureSlots = make(chan struct{}, 64)
var errCaptureLimit = errors.New("origin capture capacity exceeded")

func (w *capturedResponse) configureCapture(h *Handler, key string) {
	w.storage = h.storage
	w.limit = h.MaxBodyBytes
	if w.limit == 0 {
		w.limit = defaultCaptureLimit
	}
	w.bypass = func(header http.Header, status int) {
		h.prepareResponse(header, status)
		h.setResultHeaders(header, "BYPASS", key, 0)
	}
}

func (w *capturedResponse) reserveCapture(total uint64) bool {
	// Reserve space for both the raw spool and its encoded staging copy,
	// including block rounding and object/header overhead.
	if total > (^uint64(0)-8192)/2 {
		return false
	}
	// Round up to a 4 KiB block boundary; the result is always >= 2*total+4096.
	needed := ((2*total + 8191) / 4096) * 4096
	if needed <= w.reserved {
		return true
	}
	// Reserve exactly what the capture needs. Over-reserving here would hold
	// phantom bytes in the provider's admission budget, which squeezes out
	// other captures under concurrency; the statfs cost of frequent small
	// reservations is absorbed by the provider's admission snapshot cache
	// instead. w.reserved always tracks the exact amount passed to
	// ReserveCapture, so discardCapture releases it without leaking.
	delta := needed - w.reserved
	if w.storage != nil && !w.storage.ReserveCapture(delta) {
		return false
	}
	w.reserved += delta
	return true
}

func (w *capturedResponse) discardCapture() {
	w.discarded = true
	if w.file != nil {
		_ = w.file.Close()
		_ = os.Remove(w.path)
		w.file = nil
	}
	if w.storage != nil {
		w.storage.ReleaseCapture(w.reserved)
	}
	w.reserved = 0
}

// On resource exhaustion, a foreground response switches to forwarding.
// Background refresh has no consumer, so abort it and retain the old entry.
func (w *capturedResponse) stopCapture() error {
	if !w.streamed {
		if w.downstream == nil {
			// Background refresh has no client to forward to. The capture is
			// not discarded here (unlike the foreground path below): the
			// caller's deferred Close releases the reservation and spool file.
			w.captureErr = errCaptureLimit
			return errCaptureLimit
		}
		if w.bypass != nil {
			w.bypass(w.header, w.Status())
		}
		status := w.Status()
		if conditional := httpconditional.Status(w.clientRequest, &http.Response{StatusCode: status, Header: w.header}); conditional != 0 {
			status = conditional
			w.responseStatus = conditional
			w.suppressBody = true
			w.header.Del("Content-Length")
			w.header.Del("Content-Range")
		}
		transferHeader(w.downstream.Header(), w.header)
		w.downstream.WriteHeader(status)
		w.streamed = true
		if !w.suppressBody {
			if _, err := w.file.Seek(0, io.SeekStart); err != nil {
				return err
			}
			if _, err := io.Copy(w.downstream, w.file); err != nil {
				w.downstreamErr = err
			}
		}
	}
	w.discardCapture()
	return nil
}
