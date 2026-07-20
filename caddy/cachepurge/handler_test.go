package cachepurge

import (
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestHandlerReturnsErrorForInactiveProvider(t *testing.T) {
	handler := Handler{Path: t.TempDir(), Hosts: []string{"example.test"}}
	response := httptest.NewRecorder()
	request := httptest.NewRequest("PURGE", "http://example.test/item", nil)
	if err := handler.ServeHTTP(response, request, caddyhttp.Handler(nil)); err == nil {
		t.Fatal("expected inactive provider error")
	}
}
