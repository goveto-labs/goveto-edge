package site

import (
	"fmt"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// NormalizeHostHeader trims and validates an optional origin Host header.
func NormalizeHostHeader(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" && !httpguts.ValidHostHeader(value) {
		return "", fmt.Errorf("invalid origin host_header")
	}
	return value, nil
}
