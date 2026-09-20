package govetocache

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestOriginCaptureRespectsObjectAndCapacityLimits(t *testing.T) {
	for _, maxBody := range []uint64{1024, 4 << 20} {
		for _, known := range []bool{false, true} {
			for _, stale := range []bool{false, true} {
				t.Run(fmt.Sprintf("limit=%d/known=%t/stale=%t", maxBody, known, stale), func(t *testing.T) {
					storage, dir := newTestCache(t, time.Minute) // 1 MiB total
					h := &Handler{SiteID: "capture", storage: storage, Path: dir, DefaultTTL: 60, MaxBodyBytes: maxBody}
					request := httptest.NewRequest("GET", "http://example.test/large", nil)
					if stale {
						prime := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
							w.Header().Set("ETag", `"old"`)
							_, err := io.WriteString(w, "old")
							return err
						})
						if err := h.ServeHTTP(httptest.NewRecorder(), request, prime); err != nil {
							t.Fatal(err)
						}
						expireTestEntry(t, h, request)
					}
					body := strings.Repeat("x", 2<<20)
					var maxCaptured int64
					next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
						if known {
							w.Header().Set("Content-Length", strconv.Itoa(len(body)))
						}
						for offset := 0; offset < len(body); offset += 512 {
							if _, err := io.WriteString(w, body[offset:offset+512]); err != nil {
								return err
							}
							paths, err := filepath.Glob(filepath.Join(dir, ".goveto-origin-*"))
							if err != nil {
								return err
							}
							for _, path := range paths {
								info, err := os.Stat(path)
								if err != nil {
									return err
								}
								if info.Size() > maxCaptured {
									maxCaptured = info.Size()
								}
							}
						}
						return nil
					})
					response := httptest.NewRecorder()
					if err := h.ServeHTTP(response, request, next); err != nil {
						t.Fatal(err)
					}
					if uint64(maxCaptured) > min(maxBody, 1<<20) {
						t.Fatalf("capture grew to %d", maxCaptured)
					}
					if response.Code != http.StatusOK || response.Body.String() != body {
						t.Fatalf("status=%d forwarded bytes=%d", response.Code, response.Body.Len())
					}
					baseKey := h.storageKey(h.cacheKey(request, nil))
					if stale && maxBody >= uint64(len(body)) {
						// The rejection is a transient capacity shortfall, not
						// a property of the response: the cacheable stale
						// fallback entry must survive.
						fresh, staleEntry, _ := storage.LookupEntry(baseKey, request)
						if fresh != nil {
							_ = fresh.Body.Close()
						}
						if staleEntry == nil {
							t.Fatal("stale fallback entry deleted on transient capacity failure")
						}
						_ = staleEntry.Body.Close()
						// The retained entry occupies part of the budget; a
						// half-budget reservation still detects a leak.
						if !storage.ReserveCapture(512 << 10) {
							t.Fatal("capture reservation leaked")
						}
						storage.ReleaseCapture(512 << 10)
					} else {
						assertNoBodyObjects(t, dir)
						if !storage.ReserveCapture(1 << 20) {
							t.Fatal("capture reservation leaked")
						}
						storage.ReleaseCapture(1 << 20)
					}
				})
			}
		}
	}
}

func TestCaptureSlotsExhaustedPassesThrough(t *testing.T) {
	storage, dir := newTestCache(t, time.Minute)
	h := &Handler{SiteID: "capture", storage: storage, Path: dir, DefaultTTL: 60, XCache: true}
	request := httptest.NewRequest("GET", "http://example.test/slots", nil)
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Length", "4")
		_, err := io.WriteString(w, "body")
		return err
	})

	// Saturate the process-wide capture slots as concurrent captures would.
	for i := 0; i < cap(captureSlots); i++ {
		captureSlots <- struct{}{}
	}
	drained := false
	drain := func() {
		if drained {
			return
		}
		drained = true
		for i := 0; i < cap(captureSlots); i++ {
			<-captureSlots
		}
	}
	defer drain()

	// While slots are exhausted, concurrent requests degrade to plain proxying.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := httptest.NewRecorder()
			if err := h.ServeHTTP(response, request, next); err != nil {
				t.Errorf("ServeHTTP: %v", err)
				return
			}
			if response.Code != http.StatusOK || response.Body.String() != "body" {
				t.Errorf("status=%d body=%q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("X-Cache"); got != "" {
				t.Errorf("passthrough response carried X-Cache=%q", got)
			}
		}()
	}
	wg.Wait()
	drain()

	baseKey := h.storageKey(h.cacheKey(request, nil))
	assertNoCacheEntry(t, storage, baseKey, request)
	assertNoBodyObjects(t, dir)

	// With slots free again the same request caches normally.
	followup := httptest.NewRecorder()
	if err := h.ServeHTTP(followup, request, next); err != nil {
		t.Fatal(err)
	}
	if got := followup.Header().Get("X-Cache"); got != "MISS" {
		t.Fatalf("followup X-Cache=%q, want MISS", got)
	}
}

func TestRefreshCaptureLimitDeleteDependsOnRejectedSize(t *testing.T) {
	for _, test := range []struct {
		name         string
		maxBodyBytes uint64
		wantEntry    bool
	}{
		// The reservation fails against the 1 MiB storage budget, but the
		// response itself is cacheable: keep the stale fallback entry.
		{name: "capacity shortfall keeps cacheable entry", maxBodyBytes: 0, wantEntry: true},
		// The response exceeds max_body_bytes: it could never be cached, so
		// the stale entry is dropped like any uncacheable refresh.
		{name: "over-limit refresh drops entry", maxBodyBytes: 1024, wantEntry: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, dir := newTestCache(t, time.Minute) // 1 MiB total
			h := &Handler{SiteID: "capture", storage: storage, Path: dir, DefaultTTL: 60, MaxBodyBytes: test.maxBodyBytes}
			request := httptest.NewRequest("GET", "http://example.test/refresh-large", nil)
			prime := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				w.Header().Set("ETag", `"old"`)
				_, err := io.WriteString(w, "old")
				return err
			})
			if err := h.ServeHTTP(httptest.NewRecorder(), request, prime); err != nil {
				t.Fatal(err)
			}
			expireTestEntry(t, h, request)

			body := strings.Repeat("x", 2<<20)
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				w.Header().Set("ETag", `"new"`)
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				_, err := io.WriteString(w, body)
				return err
			})
			baseRaw := h.cacheKey(request, nil)
			baseKey := h.storageKey(baseRaw)
			h.refresh(request, next, baseRaw, baseKey)

			if !test.wantEntry {
				assertNoCacheEntry(t, storage, baseKey, request)
				assertNoBodyObjects(t, dir)
				if !storage.ReserveCapture(1 << 20) {
					t.Fatal("capture reservation leaked")
				}
				storage.ReleaseCapture(1 << 20)
			} else {
				fresh, stale, _ := storage.LookupEntry(baseKey, request)
				if fresh != nil {
					_ = fresh.Body.Close()
					t.Fatal("unexpected fresh entry after rejected refresh")
				}
				if stale == nil {
					t.Fatal("stale fallback entry deleted on transient capture failure")
				}
				_ = stale.Body.Close()
				// The retained entry occupies part of the budget; a
				// half-budget reservation still detects a leak.
				if !storage.ReserveCapture(512 << 10) {
					t.Fatal("capture reservation leaked")
				}
				storage.ReleaseCapture(512 << 10)
			}
		})
	}
}

func TestOverLimitResponseCarriesBypassHeader(t *testing.T) {
	storage, dir := newTestCache(t, time.Minute)
	h := &Handler{SiteID: "capture", storage: storage, Path: dir, DefaultTTL: 60, MaxBodyBytes: 1024, XCache: true}
	request := httptest.NewRequest("GET", "http://example.test/toolarge", nil)
	// A stale entry disables early streaming, so the over-limit switch to
	// forwarding happens through stopCapture's bypass hook.
	prime := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("ETag", `"old"`)
		_, err := io.WriteString(w, "old")
		return err
	})
	if err := h.ServeHTTP(httptest.NewRecorder(), request, prime); err != nil {
		t.Fatal(err)
	}
	expireTestEntry(t, h, request)

	body := strings.Repeat("y", 4096)
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		// No Content-Length: the limit trips mid-stream via stopCapture.
		for offset := 0; offset < len(body); offset += 512 {
			if _, err := io.WriteString(w, body[offset:offset+512]); err != nil {
				return err
			}
		}
		return nil
	})
	response := httptest.NewRecorder()
	if err := h.ServeHTTP(response, request, next); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Body.String() != body {
		t.Fatalf("status=%d forwarded bytes=%d", response.Code, response.Body.Len())
	}
	if got := response.Header().Get("X-Cache"); got != "BYPASS" {
		t.Fatalf("X-Cache=%q, want BYPASS", got)
	}
	assertNoBodyObjects(t, dir)
}
