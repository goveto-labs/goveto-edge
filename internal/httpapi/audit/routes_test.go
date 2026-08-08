package auditapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/storage/gen/model"
)

func newContext(query url.Values) (*echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	return c, rec
}

func TestBuildAuditFiltersSupportsAllFields(t *testing.T) {
	c, _ := newContext(url.Values{
		"actor":         {"alice"},
		"action":        {"site.update"},
		"resource_type": {"site"},
		"resource_id":   {"site-1"},
		"request_id":    {"req-1"},
		"result":        {"FAILURE"},
	})
	wheres := buildAuditFilters(c) // Expect 7 without time bounds.
	if len(wheres) != 6 {
		t.Fatalf("expected 6 filters, got %d", len(wheres))
	}
}

func TestBuildAuditFiltersAppliesTimeBounds(t *testing.T) {
	c, _ := newContext(url.Values{
		"from": {"2026-01-02T00:00:00Z"},
		"to":   {"2026-01-31T00:00:00Z"},
	})
	wheres := buildAuditFilters(c)
	if len(wheres) != 2 {
		t.Fatalf("expected 2 time filters, got %d", len(wheres))
	}
	if wheres[1].Operator != "<" {
		t.Fatalf("to filter operator=%q, want exclusive upper bound", wheres[1].Operator)
	}
}

func TestParseTimeQueryAcceptsRFC3339AndDateOnly(t *testing.T) {
	c, _ := newContext(url.Values{"from": {"2026-03-04T05:06:07Z"}, "to": {"2026-03-04"}})
	from := parseTimeQuery(c, "from")
	to := parseTimeQuery(c, "to")
	if !from.Equal(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)) {
		t.Fatalf("from parsed wrong: %v", from)
	}
	// `to` is an exclusive bound, so a date-only value becomes next midnight.
	if !to.Equal(time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("to should become next midnight: %v", to)
	}
}

func TestParseTimeQueryIgnoresGarbage(t *testing.T) {
	c, _ := newContext(url.Values{"from": {"not-a-date"}})
	if parsed := parseTimeQuery(c, "from"); !parsed.IsZero() {
		t.Fatalf("garbage date was parsed: %v", parsed)
	}
}

func TestToAuditEventIncludesUserAndFailureReason(t *testing.T) {
	actorID := "user-1"
	reason := "permission denied"
	before := json.RawMessage(`{"x":1}`)
	entry := model.AuditLog{
		Id: "log-1", ActorId: &actorID, Actor: "alice@example.com",
		Action: "site.update", ResourceType: "site", ResourceId: "site-1", Result: "FAILURE",
		FailureReason: &reason, BeforeJson: &before, CreatedAt: time.Now(),
		User: &model.User{Id: "user-1", Email: "alice@example.com", Name: "Alice"},
	}
	event := toAuditEvent(&entry)
	if event.ActorID != "user-1" || event.FailureReason != reason || event.User == nil || event.User.Email != "alice@example.com" {
		t.Fatalf("event missing actor/user/failure: %#v", event)
	}
	if string(event.Before) != `{"x":1}` {
		t.Fatalf("before snapshot lost: %s", event.Before)
	}
}
