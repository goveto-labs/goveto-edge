// Package httpconditional evaluates request preconditions for stored responses.
package httpconditional

import (
	"net/http"
	"strings"
)

// Status returns 304/412 when a precondition ends the request, or zero.
// A nil request or response, or a non-2xx response, yields zero.
// Evaluate conditions before selecting a byte range (RFC 9110 section 13.2.2).
func Status(request *http.Request, response *http.Response) int {
	if request == nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0
	}
	get := request.Method == http.MethodGet || request.Method == http.MethodHead
	etag := response.Header.Get("ETag")
	modified, modifiedErr := http.ParseTime(response.Header.Get("Last-Modified"))
	if value := strings.Join(request.Header.Values("If-Match"), ","); value != "" {
		if !matchETags(value, etag, false) {
			return http.StatusPreconditionFailed
		}
	} else if value := request.Header.Get("If-Unmodified-Since"); value != "" && modifiedErr == nil {
		if since, err := http.ParseTime(value); err == nil && modified.After(since) {
			return http.StatusPreconditionFailed
		}
	}
	if value := strings.Join(request.Header.Values("If-None-Match"), ","); value != "" {
		if matchETags(value, etag, true) {
			if get {
				return http.StatusNotModified
			}
			return http.StatusPreconditionFailed
		}
	} else if get && modifiedErr == nil {
		if since, err := http.ParseTime(request.Header.Get("If-Modified-Since")); err == nil && !modified.After(since) {
			return http.StatusNotModified
		}
	}
	return 0
}

func RangeAllowed(request *http.Request, header http.Header) bool {
	value := request.Header.Get("If-Range")
	if value == "" {
		return true
	}
	if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "W/") {
		return !strings.HasPrefix(value, "W/") && value == header.Get("ETag")
	}
	date, err := http.ParseTime(value)
	modified, modifiedErr := http.ParseTime(header.Get("Last-Modified"))
	return err == nil && modifiedErr == nil && modified.Equal(date)
}

func matchETags(list, current string, weak bool) bool {
	if strings.TrimSpace(list) == "*" {
		return true
	}
	for list != "" {
		list = strings.TrimLeft(list, " \t,")
		prefix := ""
		if strings.HasPrefix(list, "W/") {
			prefix, list = "W/", list[2:]
		}
		if !strings.HasPrefix(list, "\"") {
			return false
		}
		end := strings.IndexByte(list[1:], '"')
		if end < 0 {
			return false
		}
		candidate := prefix + list[:end+2]
		list = list[end+2:]
		if weak && strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(current, "W/") {
			return true
		}
		if !weak && !strings.HasPrefix(candidate, "W/") && candidate == current {
			return true
		}
	}
	return false
}
