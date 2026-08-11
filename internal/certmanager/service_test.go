package certmanager

import (
	"strings"
	"testing"
)

func TestReconcileTerminalCertificateJobsCastsStatusCase(t *testing.T) {
	for _, fragment := range []string{
		"ELSE c.status::text END",
		`ELSE 'RENEWAL_FAILED' END)::"CertificateStatus"`,
	} {
		if !strings.Contains(reconcileTerminalCertificateJobsSQL, fragment) {
			t.Fatalf("reconciliation SQL missing enum-safe status expression %q: %s", fragment, reconcileTerminalCertificateJobsSQL)
		}
	}
	if strings.Contains(reconcileTerminalCertificateJobsSQL, "ELSE c.status END") {
		t.Fatalf("reconciliation SQL still mixes CertificateStatus with text: %s", reconcileTerminalCertificateJobsSQL)
	}
}
