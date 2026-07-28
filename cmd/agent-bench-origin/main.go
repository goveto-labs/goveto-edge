package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	address := flag.String("listen", ":8080", "listen address")
	delay := flag.Duration("delay", 0, "fixed response delay")
	flag.Parse()
	var requests atomic.Uint64
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		size, err := responseSize(request.URL.Path)
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		if *delay > 0 {
			time.Sleep(*delay)
		}
		count := requests.Add(1)
		response.Header().Set("Content-Type", "application/octet-stream")
		response.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=60, stale-if-error=60")
		response.Header().Set("X-Origin-Requests", strconv.FormatUint(count, 10))
		payload := make([]byte, size)
		http.ServeContent(response, request, "payload.bin", time.Unix(1_700_000_000, 0), bytes.NewReader(payload))
	})
	server := &http.Server{Addr: *address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("benchmark origin listening on %s", *address)
	log.Fatal(server.ListenAndServe())
}

func responseSize(path string) (int, error) {
	value := strings.TrimPrefix(path, "/bytes/")
	if value == path {
		return 16 << 10, nil
	}
	size, err := strconv.Atoi(value)
	if err != nil || size < 0 || size > 16<<20 {
		return 0, fmt.Errorf("invalid response size %q", value)
	}
	return size, nil
}
