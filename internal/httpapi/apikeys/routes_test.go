package apikeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/apikey"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type apiKeyUpdaterFunc func(context.Context, string, string, ...query.ClusterApiKeySetClause) (*model.ClusterApiKey, *model.ClusterApiKey, error)

func (fn apiKeyUpdaterFunc) Update(ctx context.Context, clusterID, keyID string, sets ...query.ClusterApiKeySetClause) (*model.ClusterApiKey, *model.ClusterApiKey, error) {
	return fn(ctx, clusterID, keyID, sets...)
}

func TestParsePermissions(t *testing.T) {
	permissions, err := parsePermissions([]string{"cluster.read", "site.publish", "cluster.read"})
	if err != nil {
		t.Fatal(err)
	}
	if len(permissions) != 2 {
		t.Fatalf("duplicates must collapse: %v", permissions)
	}

	for _, invalid := range [][]string{
		{},
		nil,
		{"cluster.read", "platform.user.manage"},
		{"node.manage"},
		{"cluster.read", "made.up.permission"},
		{""},
	} {
		if _, err = parsePermissions(invalid); err == nil {
			t.Errorf("input %v must be rejected", invalid)
		}
	}
}

func TestResolveExpiry(t *testing.T) {
	if _, err := resolveExpiry(nil, nil); err != nil {
		t.Fatalf("no expiry must be allowed: %v", err)
	}

	days := 30
	expiresAt, err := resolveExpiry(nil, &days)
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(*expiresAt); remaining < 29*24*time.Hour || remaining > 31*24*time.Hour {
		t.Fatalf("expires_in_days=30 produced %v", remaining)
	}

	timestamp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	expiresAt, err = resolveExpiry(&timestamp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(*expiresAt) <= 0 {
		t.Fatalf("future timestamp must be accepted: %v", expiresAt)
	}

	// Both fields at once, past timestamps, bad formats and out-of-range day
	// counts must all be rejected.
	if _, err = resolveExpiry(&timestamp, &days); err == nil {
		t.Fatal("providing both fields must be rejected")
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err = resolveExpiry(&past, nil); err == nil {
		t.Fatal("past timestamp must be rejected")
	}
	if _, err = resolveExpiry(strptr("next tuesday"), nil); err == nil {
		t.Fatal("malformed timestamp must be rejected")
	}
	zero := 0
	if _, err = resolveExpiry(nil, &zero); err == nil {
		t.Fatal("expires_in_days=0 must be rejected")
	}
	huge := 3651
	if _, err = resolveExpiry(nil, &huge); err == nil {
		t.Fatal("expires_in_days=3651 must be rejected")
	}
}

func TestTranslateError(t *testing.T) {
	cases := []struct {
		err       error
		wantCode  int
		wantError bool
	}{
		{apikey.ErrNameTaken, http.StatusConflict, true},
		{apikey.ErrClusterNotFound, http.StatusNotFound, true},
		{fmt.Errorf("wrapped: %w", apikey.ErrNotFound), http.StatusNotFound, true},
		{apikey.ErrRevoked, http.StatusConflict, true},
		{apikey.ErrExpired, http.StatusConflict, true},
		{apikey.ErrCreatorInactive, http.StatusConflict, true},
		{apikey.ErrPermissionNotAllowed, http.StatusBadRequest, true},
		{nil, 0, false},
	}
	for _, testCase := range cases {
		translated := translateError(testCase.err)
		httpError, ok := translated.(*echo.HTTPError)
		if ok != testCase.wantError {
			t.Fatalf("translateError(%v) = %#v, want HTTPError=%v", testCase.err, translated, testCase.wantError)
		}
		if ok && httpError.Code != testCase.wantCode {
			t.Fatalf("translateError(%v) code=%d, want %d", testCase.err, httpError.Code, testCase.wantCode)
		}
	}

	infrastructureError := errors.New("permission denied for relation cluster_api_keys")
	if translated := translateError(infrastructureError); translated != infrastructureError {
		t.Fatalf("infrastructure error was reclassified or exposed: %#v", translated)
	}
}

func TestUpdateRejectsConflictingExpiryFields(t *testing.T) {
	called := false
	handler := update(apiKeyUpdaterFunc(func(context.Context, string, string, ...query.ClusterApiKeySetClause) (*model.ClusterApiKey, *model.ClusterApiKey, error) {
		called = true
		return nil, nil, nil
	}))
	for _, body := range []string{
		`{"clear_expiry":true,"expires_at":"2030-01-01T00:00:00Z"}`,
		`{"clear_expiry":true,"expires_in_days":30}`,
	} {
		request := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		context := echo.New().NewContext(request, httptest.NewRecorder())
		err := handler(context)
		var httpError *echo.HTTPError
		if !errors.As(err, &httpError) || httpError.Code != http.StatusBadRequest {
			t.Fatalf("body %s: error=%v, want 400", body, err)
		}
	}
	if called {
		t.Fatal("conflicting expiry request reached the service")
	}
}

func TestUpdateHandlerTranslatesScopedLifecycleErrors(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		serviceErr error
		wantStatus int
	}{
		{name: "key from another cluster", serviceErr: apikey.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "revoked key", serviceErr: apikey.ErrRevoked, wantStatus: http.StatusConflict},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			handler := update(apiKeyUpdaterFunc(func(_ context.Context, clusterID, keyID string, _ ...query.ClusterApiKeySetClause) (*model.ClusterApiKey, *model.ClusterApiKey, error) {
				if clusterID != "cluster-a" || keyID != "key-b" {
					t.Fatalf("scope = %q/%q", clusterID, keyID)
				}
				return nil, nil, testCase.serviceErr
			}))
			request := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"name":"updated"}`))
			request.Header.Set("Content-Type", "application/json")
			context := echo.New().NewContext(request, httptest.NewRecorder())
			context.SetPathValues(echo.PathValues{
				{Name: "cluster_id", Value: "cluster-a"},
				{Name: "key_id", Value: "key-b"},
			})
			err := handler(context)
			var httpError *echo.HTTPError
			if !errors.As(err, &httpError) || httpError.Code != testCase.wantStatus {
				t.Fatalf("error=%v, want HTTP %d", err, testCase.wantStatus)
			}
		})
	}
}

func TestListRejectsInvalidStatusBeforeDatabaseAccess(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/?status=expired", nil)
	context := echo.New().NewContext(request, httptest.NewRecorder())
	err := list(nil)(context)
	var httpError *echo.HTTPError
	if !errors.As(err, &httpError) || httpError.Code != http.StatusBadRequest {
		t.Fatalf("error=%v, want 400", err)
	}
}

func TestDisabledStatusClearsRotationGrace(t *testing.T) {
	sets := statusSets(model.ApiKeyStatusDISABLED)
	want := map[string]bool{"status": false, "previous_token_hash": false, "previous_expires_at": false}
	for _, set := range sets {
		if _, found := want[set.Field]; found {
			want[set.Field] = true
		}
	}
	for field, found := range want {
		if !found {
			t.Errorf("disabled status update is missing %s", field)
		}
	}
	if sets := statusSets(model.ApiKeyStatusACTIVE); len(sets) != 1 || sets[0].Field != "status" {
		t.Fatalf("active status unexpectedly changes grace fields: %#v", sets)
	}
}

func TestResolveRotationGraceChecksIntegerBeforeDurationConversion(t *testing.T) {
	if grace, err := resolveRotationGrace(nil); err != nil || grace != apikey.DefaultRotationGrace {
		t.Fatalf("default grace = %v, %v", grace, err)
	}
	valid := int(apikey.MaxRotationGrace / time.Second)
	if grace, err := resolveRotationGrace(&valid); err != nil || grace != apikey.MaxRotationGrace {
		t.Fatalf("maximum grace = %v, %v", grace, err)
	}
	for _, value := range []int{-1, valid + 1, int(^uint(0) >> 1)} {
		if _, err := resolveRotationGrace(&value); err == nil {
			t.Errorf("out-of-range grace %d was accepted", value)
		} else if !strings.Contains(err.Error(), fmt.Sprint(valid)) {
			t.Errorf("grace error %q does not reflect MaxRotationGrace=%d", err, valid)
		}
	}
}

func TestNewResourceNeverExposesHashes(t *testing.T) {
	previous := "previous-hash"
	key := &model.ClusterApiKey{
		Id: "key", ClusterId: "cluster", Name: "terraform", Prefix: "gve1_Ab12cDe",
		TokenHash: "secret-hash", PreviousTokenHash: &previous,
		PermissionsJson: json.RawMessage(`["cluster.read"]`),
		Status:          model.ApiKeyStatusACTIVE, CreatedBy: "creator",
	}
	resource := newResource(key)
	encoded, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-hash") || strings.Contains(string(encoded), "previous-hash") {
		t.Fatalf("resource leaked a token hash: %s", encoded)
	}
	if len(resource.Permissions) != 1 || resource.Permissions[0] != "cluster.read" {
		t.Fatalf("permissions projection wrong: %v", resource.Permissions)
	}
	if resource.Prefix != "gve1_Ab12cDe" {
		t.Fatalf("prefix projection wrong: %q", resource.Prefix)
	}
}

func strptr(value string) *string { return &value }
