package nodes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/storage/gen/model"
)

func TestRetryAgentUpgradeRejectsDevelopmentBuild(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/agent-upgrade/retry", nil)
	context := echo.NewContext(request, httptest.NewRecorder())
	err := retryAgentUpgrade(nil, nil)(context)
	assertHTTPStatus(t, err, http.StatusConflict)
}

func TestValidateAgentUpgradeRetryNode(t *testing.T) {
	credentialID := "credential-1"
	host := "192.0.2.10"
	port := 22
	oldVersion := "v1.2.2"
	newVersion := "v1.2.3"
	valid := func() *model.Node {
		return &model.Node{
			ClusterId: "cluster-1", Status: model.NodeStatusONLINE, Version: &oldVersion,
			SshCredentialId: &credentialID, SshHost: &host, SshPort: &port,
		}
	}

	assertHTTPStatus(t, validateAgentUpgradeRetryNode(nil, "cluster-1", newVersion), http.StatusNotFound)
	assertHTTPStatus(t, validateAgentUpgradeRetryNode(valid(), "cluster-2", newVersion), http.StatusNotFound)

	offline := valid()
	offline.Status = model.NodeStatusOFFLINE
	assertHTTPStatus(t, validateAgentUpgradeRetryNode(offline, "cluster-1", newVersion), http.StatusConflict)

	missingSSH := valid()
	missingSSH.SshHost = nil
	assertHTTPStatus(t, validateAgentUpgradeRetryNode(missingSSH, "cluster-1", newVersion), http.StatusConflict)

	invalidPort := valid()
	invalidPort.SshPort = intPointerForAgentUpgrade(65536)
	assertHTTPStatus(t, validateAgentUpgradeRetryNode(invalidPort, "cluster-1", newVersion), http.StatusConflict)

	upToDate := valid()
	upToDate.Version = &newVersion
	assertHTTPStatus(t, validateAgentUpgradeRetryNode(upToDate, "cluster-1", newVersion), http.StatusConflict)

	if err := validateAgentUpgradeRetryNode(valid(), "cluster-1", newVersion); err != nil {
		t.Fatalf("valid upgrade retry rejected: %v", err)
	}
}

func assertHTTPStatus(t *testing.T, err error, want int) {
	t.Helper()
	httpError, ok := err.(*echo.HTTPError)
	if !ok || httpError.Code != want {
		t.Fatalf("HTTP error = %#v; want status %d", err, want)
	}
}

func intPointerForAgentUpgrade(value int) *int { return &value }
