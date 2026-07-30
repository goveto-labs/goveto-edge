package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	address := flag.String("listen", ":8080", "listen address")
	delay := flag.Duration("delay", 0, "fixed response delay")
	originID := flag.String("id", "origin-1", "value returned in X-Benchmark-Origin")
	failPrefix := flag.String("fail-prefix", "", "return 503 for paths with this prefix")
	flag.Parse()
	var requests atomic.Uint64
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		if *failPrefix != "" && strings.HasPrefix(request.URL.Path, *failPrefix) {
			http.Error(response, "benchmark origin failure", http.StatusServiceUnavailable)
			return
		}
		behavior, size, err := parseRequestPath(request.URL.Path)
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		responseDelay := *delay
		if behavior.delay > 0 {
			responseDelay = behavior.delay
		}
		if responseDelay > 0 {
			time.Sleep(responseDelay)
		}
		count := requests.Add(1)
		response.Header().Set("Content-Type", "application/octet-stream")
		response.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=60, stale-if-error=60")
		response.Header().Set("X-Origin-Requests", strconv.FormatUint(count, 10))
		response.Header().Set("X-Benchmark-Origin", *originID)
		var payload []byte
		if behavior.pattern {
			payload = deterministicPayload(size)
		} else {
			payload = make([]byte, size)
		}
		if behavior.bytesPerSecond > 0 && request.Header.Get("Range") == "" {
			response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			writeThrottled(response, payload, behavior.bytesPerSecond)
			return
		}
		http.ServeContent(response, request, "payload.bin", time.Unix(1_700_000_000, 0), bytes.NewReader(payload))
	})
	server := &http.Server{Addr: *address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("benchmark origin listening on %s", *address)
	log.Fatal(server.ListenAndServe())
}

type responseBehavior struct {
	delay          time.Duration
	bytesPerSecond int
	pattern        bool
}

var deterministicPayloads sync.Map

func parseRequestPath(requestPath string) (responseBehavior, int, error) {
	parts := strings.Split(strings.Trim(path.Clean(requestPath), "/"), "/")
	if len(parts) == 2 && parts[0] == "bytes" {
		size, err := parseSize(parts[1])
		return responseBehavior{}, size, err
	}
	if len(parts) == 2 && parts[0] == "pattern" {
		size, err := parseSize(parts[1])
		return responseBehavior{pattern: true}, size, err
	}
	if len(parts) == 4 && parts[0] == "delay" && parts[2] == "bytes" {
		milliseconds, err := strconv.Atoi(parts[1])
		if err != nil || milliseconds < 0 || milliseconds > 60000 {
			return responseBehavior{}, 0, fmt.Errorf("invalid delay %q", parts[1])
		}
		size, sizeErr := parseSize(parts[3])
		return responseBehavior{delay: time.Duration(milliseconds) * time.Millisecond}, size, sizeErr
	}
	if len(parts) == 4 && parts[0] == "throttle" && parts[2] == "bytes" {
		rate, err := strconv.Atoi(parts[1])
		if err != nil || rate < 1024 || rate > 1<<30 {
			return responseBehavior{}, 0, fmt.Errorf("invalid throttle rate %q", parts[1])
		}
		size, sizeErr := parseSize(parts[3])
		return responseBehavior{bytesPerSecond: rate}, size, sizeErr
	}
	return responseBehavior{}, 16 << 10, nil
}

func deterministicPayload(size int) []byte {
	if payload, ok := deterministicPayloads.Load(size); ok {
		return payload.([]byte)
	}
	payload := make([]byte, size)
	state := uint64(0x9e3779b97f4a7c15)
	for index := range payload {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		payload[index] = byte(state)
	}
	actual, _ := deterministicPayloads.LoadOrStore(size, payload)
	return actual.([]byte)
}

func parseSize(value string) (int, error) {
	size, err := strconv.Atoi(value)
	if err != nil || size < 0 || size > 16<<20 {
		return 0, fmt.Errorf("invalid response size %q", value)
	}
	return size, nil
}

func writeThrottled(response http.ResponseWriter, payload []byte, bytesPerSecond int) {
	const chunkSize = 32 << 10
	started := time.Now()
	written := 0
	for written < len(payload) {
		end := min(written+chunkSize, len(payload))
		count, err := response.Write(payload[written:end])
		written += count
		if err != nil {
			return
		}
		target := time.Duration(float64(written) / float64(bytesPerSecond) * float64(time.Second))
		if wait := target - time.Since(started); wait > 0 {
			time.Sleep(wait)
		}
	}
}
