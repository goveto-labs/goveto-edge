package initialization

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/testutil"
)

func TestInitializationTokenLifecycleIntegration(t *testing.T) {
	db := testutil.Database(t)
	e := echo.New()
	e.POST("/", initialize(db, settings.New(db, nil), nil, nil, "operator-secret"))
	call := func(token, email string) int {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"initialization_token":"`+token+`","email":"`+email+`","name":"Admin","password":"long-test-password","agent_gateway_public_address":"gateway.example.test:8443"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		return response.Code
	}
	if got := call("", "admin@example.test"); got != http.StatusForbidden {
		t.Fatalf("missing token status=%d", got)
	}
	if got := call("wrong-secret", "admin@example.test"); got != http.StatusForbidden {
		t.Fatalf("wrong token status=%d", got)
	}
	if got := call("operator-secret", ""); got != http.StatusBadRequest {
		t.Fatalf("missing email status=%d", got)
	}
	if got := call("operator-secret", "Admin@Example.Test"); got != http.StatusCreated {
		t.Fatalf("setup status=%d", got)
	}
	if got := call("operator-secret", "admin@example.test"); got != http.StatusConflict {
		t.Fatalf("replay status=%d", got)
	}
	// Once initialized the 409 semantics win over token validation, so a
	// replay with a wrong token after restart still reports conflict.
	if got := call("wrong-secret", "admin@example.test"); got != http.StatusConflict {
		t.Fatalf("replay with wrong token status=%d", got)
	}
	initialized, err := settings.New(db, nil).Initialized(context.Background())
	if err != nil || !initialized {
		t.Fatalf("initialized=%t err=%v", initialized, err)
	}
	users, err := db.User.Query().Do(context.Background())
	if err != nil || len(users) != 1 {
		t.Fatalf("users=%d err=%v", len(users), err)
	}
	if users[0].Role != model.UserRoleADMIN {
		t.Fatalf("role=%s", users[0].Role)
	}
	if users[0].Email != "admin@example.test" {
		t.Fatalf("email=%s", users[0].Email)
	}
}
