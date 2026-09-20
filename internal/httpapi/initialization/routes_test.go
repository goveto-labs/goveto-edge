package initialization

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestInitializationRequiresOperatorToken(t *testing.T) {
	for _, test := range []struct{ name, configured, supplied string }{
		{"missing", "operator-secret", ""},
		{"incorrect", "operator-secret", "attacker"},
		{"disabled", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			e.POST("/", initialize(nil, nil, nil, nil, test.configured))
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"initialization_token":"`+test.supplied+`"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			e.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d, body=%s", response.Code, response.Body)
			}
		})
	}
}
