package clusters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/storage/gen/client"
)

func apiKeyContext(method, target, body string) (*echo.Context, *httptest.ResponseRecorder) {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	auth.SetCurrentAPIKey(ctx, &auth.APIKeyPrincipal{KeyID: "key-1", ClusterID: "cluster-1", Prefix: "gve1_example"})
	return ctx, recorder
}

func TestListAvailableForAPIKeyUsesOnlyBoundCluster(t *testing.T) {
	createdAt := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	ctx, recorder := apiKeyContext(http.MethodGet, "/api/v1/clusters", "")
	handler := listAvailableWithAPIKeyChoices(nil, nil,
		func(_ context.Context, db *client.Client, principal *auth.APIKeyPrincipal) ([]clusterChoice, error) {
			if db != nil || principal.ClusterID != "cluster-1" {
				t.Fatalf("loader received db=%v principal=%#v", db, principal)
			}
			return []clusterChoice{{ID: principal.ClusterID, Name: "Bound", Role: "API_KEY", CreatedAt: createdAt}}, nil
		})
	if err := handler(ctx); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data clusterListResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Clusters) != 1 || response.Data.Clusters[0].ID != "cluster-1" ||
		response.Data.SelectedClusterID != "cluster-1" || response.Data.RequiresCluster {
		t.Fatalf("unexpected response: %#v", response.Data)
	}
}

func TestListAvailableForAPIKeyClearsDeletedClusterSelection(t *testing.T) {
	ctx, recorder := apiKeyContext(http.MethodGet, "/api/v1/clusters", "")
	handler := listAvailableWithAPIKeyChoices(nil, nil,
		func(context.Context, *client.Client, *auth.APIKeyPrincipal) ([]clusterChoice, error) {
			return []clusterChoice{}, nil
		})
	if err := handler(ctx); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data clusterListResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Clusters) != 0 || response.Data.SelectedClusterID != "" || !response.Data.RequiresCluster {
		t.Fatalf("unexpected response: %#v", response.Data)
	}
}

func TestAPIKeyCannotCreateOrSelectCluster(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		handler echo.HandlerFunc
		method  string
		path    string
		body    string
	}{
		{name: "create", handler: create(nil, nil), method: http.MethodPost, path: "/api/v1/clusters", body: `{"name":"blocked"}`},
		{name: "select", handler: selectCurrent(nil, nil), method: http.MethodPut, path: "/api/v1/session/cluster", body: `{"cluster_id":"cluster-2"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, _ := apiKeyContext(testCase.method, testCase.path, testCase.body)
			err := testCase.handler(ctx)
			httpError, ok := err.(*echo.HTTPError)
			if !ok || httpError.Code != http.StatusForbidden {
				t.Fatalf("error=%v, want 403", err)
			}
		})
	}
}
