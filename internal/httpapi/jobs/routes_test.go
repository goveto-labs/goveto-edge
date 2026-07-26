package jobs

import (
	"testing"

	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/rbac"
)

func TestParseKind(t *testing.T) {
	kind, err := parseKind("publish")
	if err != nil || kind != jobqueue.Publish {
		t.Fatalf("parse publish = %q, %v", kind, err)
	}
	if _, err = parseKind("unknown"); err == nil {
		t.Fatal("unknown job kind was accepted")
	}
}

func TestPermissionForSensitiveJobs(t *testing.T) {
	if got := permissionFor(jobqueue.Install); got != rbac.PermissionNodeManage {
		t.Fatalf("install permission = %q", got)
	}
	if got := permissionFor(jobqueue.Certificate); got != rbac.PermissionCertificateManage {
		t.Fatalf("certificate permission = %q", got)
	}
}
