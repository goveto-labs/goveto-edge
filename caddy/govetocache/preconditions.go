package govetocache

import "net/http"

// Origin revalidation validates the stored representation. Client conditions
// are evaluated separately against the resulting representation.
func clearClientPreconditions(header http.Header) {
	for _, name := range []string{"If-Match", "If-Unmodified-Since", "If-None-Match", "If-Modified-Since", "If-Range"} {
		header.Del(name)
	}
}
