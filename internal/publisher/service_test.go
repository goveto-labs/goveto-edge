package publisher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"goveto-edge/internal/configseal"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/model"
)

func TestSemanticConfigHashIgnoresVersion(t *testing.T) {
	left := edgeprotocol.SiteConfig{SiteID: "site-1", Version: 4, Domains: []string{"example.test"}}
	right := left
	right.Version = 99
	leftHash := semanticConfigHash(left)
	rightHash := semanticConfigHash(right)
	if !bytes.Equal(leftHash[:], rightHash[:]) {
		t.Fatal("semantic config hash changed for a version-only update")
	}
}

func TestOriginConfigRejectsInvalidStoredHostHeader(t *testing.T) {
	hostHeader := "origin.example.com\r\nX-Injected: true"
	_, err := originConfig("site-1", model.OriginBackend{Id: "backend-1", HostHeader: &hostHeader})
	if err == nil {
		t.Fatal("invalid stored host_header was accepted")
	}
	for _, fragment := range []string{"site site-1", "origin backend backend-1", "host_header"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("origin error missing %q: %v", fragment, err)
		}
	}
}

func TestSuccessfulTargetsPartitionsResults(t *testing.T) {
	targets := []target{{NodeID: "a"}, {NodeID: "b"}, {NodeID: "c"}}
	results := []targetResult{{NodeID: "a", Success: true}, {NodeID: "b", Error: "timeout"}, {NodeID: "c", Success: true}}

	succeeded, failed := successfulTargets(results, targets)
	if len(succeeded) != 2 || succeeded[0].NodeID != "a" || succeeded[1].NodeID != "c" {
		t.Fatalf("unexpected successful targets: %#v", succeeded)
	}
	if len(failed) != 1 || failed[0].NodeID != "b" || failed[0].Error != "timeout" {
		t.Fatalf("unexpected failed results: %#v", failed)
	}
}

func TestAllTargetsRejectedErrorIncludesNodeReasons(t *testing.T) {
	err := allTargetsRejectedError([]targetResult{
		{NodeID: "node-1", Error: "invalid config"},
		{NodeID: "node-2"},
	})
	for _, fragment := range []string{
		"all nodes rejected the configuration",
		"node node-1: invalid config",
		"node node-2: unknown error",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("aggregate rejection error missing %q: %v", fragment, err)
		}
	}
}

func TestWAFChallengeSecretIsStableAndSiteScoped(t *testing.T) {
	cipher, err := node.NewCredentialCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	one := wafChallengeSecret(cipher, "site-1")
	if one != wafChallengeSecret(cipher, "site-1") || one == wafChallengeSecret(cipher, "site-2") {
		t.Fatal("WAF challenge secret is not stable and site-scoped")
	}
}

func TestAllSucceeded(t *testing.T) {
	tests := []struct {
		name    string
		results []targetResult
		want    bool
	}{
		{name: "empty rollback", want: true},
		{name: "complete", results: []targetResult{{Success: true}, {Success: true}}, want: true},
		{name: "partial", results: []targetResult{{Success: true}, {Success: false}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := allSucceeded(test.results); got != test.want {
				t.Fatalf("allSucceeded() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPublishOutcomeDoesNotRetryAfterRollback(t *testing.T) {
	result := []targetResult{{NodeID: "node-1", Success: true, RolledBack: true}}
	outcome := publishOutcome(model.JobStatusFAILED, result, errors.New("publish rejected"), true)
	if outcome.Retryable || outcome.Compensation == nil || outcome.Err == nil {
		t.Fatalf("rollback outcome = %#v", outcome)
	}
}

func TestNextPublishVersionHandlesMissingHistory(t *testing.T) {
	if got := nextPublishVersion(1, nil, nil); got != 2 {
		t.Fatalf("next version = %d, want 2", got)
	}
	pending := &model.PublishJob{Version: 7}
	if got := nextPublishVersion(2, pending, nil); got != 7 {
		t.Fatalf("pending version = %d, want 7", got)
	}
	latest := &model.ConfigVersion{Version: 9}
	if got := nextPublishVersion(3, nil, latest); got != 10 {
		t.Fatalf("version after history = %d, want 10", got)
	}
}

func TestSamePublishRequestIgnoresVersionAndTargetOrder(t *testing.T) {
	service := &Service{}
	config := edgeprotocol.SiteConfig{
		SiteID:  "site-1",
		Version: 12,
		Domains: []string{"example.test"},
	}
	existing := config
	existing.Version = 11
	existingJSON, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	existingTargets, err := json.Marshal([]target{{NodeID: "node-2"}, {NodeID: "node-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !service.samePublishRequest(
		config,
		[]target{{NodeID: "node-1"}, {NodeID: "node-2"}},
		existingJSON,
		existingTargets,
	) {
		t.Fatal("version-only change with the same targets was not coalesced")
	}
}

func TestSamePublishRequestDetectsContentOrTargetChanges(t *testing.T) {
	service := &Service{}
	existing := edgeprotocol.SiteConfig{SiteID: "site-1", Version: 11, Domains: []string{"example.test"}}
	existingJSON, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	existingTargets := []byte(`[{"node_id":"node-1"}]`)

	changed := existing
	changed.Version = 12
	changed.Domains = []string{"changed.example.test"}
	if service.samePublishRequest(changed, []target{{NodeID: "node-1"}}, existingJSON, existingTargets) {
		t.Fatal("content change was incorrectly coalesced")
	}

	sameContent := existing
	sameContent.Version = 12
	if service.samePublishRequest(sameContent, []target{{NodeID: "node-2"}}, existingJSON, existingTargets) {
		t.Fatal("target change was incorrectly coalesced")
	}
}

func sealedSnapshotService(t *testing.T) *Service {
	t.Helper()
	return sealedSnapshotServiceWithKey(t, "publisher-seal-test")
}

func sealedSnapshotServiceWithKey(t *testing.T, seed string) *Service {
	t.Helper()
	key := sha256.Sum256([]byte(seed))
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(key[:]))
	if err != nil {
		t.Fatal(err)
	}
	return &Service{configSecrets: configseal.New(cipher)}
}

func secretBearingSiteConfig() edgeprotocol.SiteConfig {
	config := edgeprotocol.SiteConfig{
		SiteID: "site-1", Version: 12, Domains: []string{"example.test"},
		WAF: map[string]any{"challenge_secret": "waf-secret"},
	}
	config.Certificates = []edgeprotocol.CertificateConfig{{CertificatePEM: "certificate-pem", PrivateKeyPEM: "private-key-pem"}}
	config.OriginPolicy.Transport.TLSClientPrivateKeyPEM = "mtls-private-key"
	return config
}

func TestSealedConfigJSONHidesSecretsAndKeepsPlaintextArgument(t *testing.T) {
	service := sealedSnapshotService(t)
	config := secretBearingSiteConfig()

	sealedJSON, err := service.sealedConfigJSON(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-key-pem", "mtls-private-key", "waf-secret"} {
		if strings.Contains(string(sealedJSON), secret) {
			t.Fatalf("secret %q persists in sealed snapshot: %s", secret, sealedJSON)
		}
	}
	if !strings.Contains(string(sealedJSON), "enc:v2:") {
		t.Fatalf("sealed snapshot carries no envelope: %s", sealedJSON)
	}
	// The plaintext argument must stay usable for hashing and comparisons.
	if config.Certificates[0].PrivateKeyPEM != "private-key-pem" || config.WAF["challenge_secret"] != "waf-secret" {
		t.Fatal("sealedConfigJSON mutated the plaintext config")
	}
	var stored edgeprotocol.SiteConfig
	if err = json.Unmarshal(sealedJSON, &stored); err != nil {
		t.Fatal(err)
	}
	if err = service.configSecrets.UnsealSiteConfigSecrets(stored.SiteID, stored.Version, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Certificates[0].PrivateKeyPEM != "private-key-pem" || stored.WAF["challenge_secret"] != "waf-secret" {
		t.Fatal("unsealed snapshot does not round-trip secrets")
	}
}

func TestSamePublishRequestCoalescesAgainstSealedStoredConfig(t *testing.T) {
	service := sealedSnapshotService(t)
	config := secretBearingSiteConfig()
	sealedJSON, err := service.sealedConfigJSON(config)
	if err != nil {
		t.Fatal(err)
	}
	targets, _ := json.Marshal([]target{{NodeID: "node-1"}, {NodeID: "node-2"}})

	// Identical plaintext config must still coalesce against the sealed row;
	// envelope nonces would otherwise make every publish look unique.
	reissued := config
	reissued.Version = 13
	if !service.samePublishRequest(reissued, []target{{NodeID: "node-2"}, {NodeID: "node-1"}}, sealedJSON, targets) {
		t.Fatal("identical publish was not coalesced against sealed snapshot")
	}

	// A changed secret must be detected after unsealing.
	rotated := config
	rotated.Version = 13
	rotated.Certificates[0].PrivateKeyPEM = "rotated-private-key-pem"
	if service.samePublishRequest(rotated, []target{{NodeID: "node-1"}, {NodeID: "node-2"}}, sealedJSON, targets) {
		t.Fatal("secret rotation was incorrectly coalesced")
	}

	// Legacy plaintext rows keep coalescing during the migration window.
	legacy, _ := json.Marshal(config)
	if !service.samePublishRequest(reissued, []target{{NodeID: "node-2"}, {NodeID: "node-1"}}, legacy, targets) {
		t.Fatal("legacy plaintext snapshot stopped coalescing")
	}
}

func TestPruneConfigVersionIDs(t *testing.T) {
	site := model.Site{Id: "site-1", Version: 8}
	makeVersions := func(statuses ...model.ConfigStatus) []model.ConfigVersion {
		versions := make([]model.ConfigVersion, 0, len(statuses))
		for index, status := range statuses {
			versions = append(versions, model.ConfigVersion{
				Id: fmt.Sprintf("v-%d", index+1), SiteId: site.Id,
				Version: int64(index + 1), Status: status,
			})
		}
		return versions
	}

	// Newest `keep` terminal rows survive; older FAILED rows are pruned and
	// DRAFT rows are never touched (pending jobs may still execute them).
	versions := makeVersions(
		model.ConfigStatusFAILED, model.ConfigStatusDRAFT,
		model.ConfigStatusPUBLISHED, model.ConfigStatusPUBLISHED,
		model.ConfigStatusPUBLISHED, model.ConfigStatusROLLED_BACK,
	)
	pruned := pruneConfigVersionIDs([]model.Site{site}, versions, 3)
	// Terminal rows newest-first: v6, v5, v4 kept; v3 outside the window is
	// covered by neither the live nor the baseline rule; v1 FAILED pruned;
	// v2 DRAFT untouched.
	if len(pruned) != 2 || pruned[0] != "v-3" || pruned[1] != "v-1" {
		t.Fatalf("pruned = %v, want [v-3 v-1]", pruned)
	}

	// A failed publish storm must not evict the newest published baseline
	// even when it falls outside the retention window.
	burst := makeVersions(
		model.ConfigStatusPUBLISHED,
		model.ConfigStatusFAILED, model.ConfigStatusFAILED, model.ConfigStatusFAILED,
		model.ConfigStatusFAILED, model.ConfigStatusFAILED,
	)
	pruned = pruneConfigVersionIDs([]model.Site{site}, burst, 3)
	for _, id := range pruned {
		if id == "v-1" {
			t.Fatalf("published baseline pruned: %v", pruned)
		}
	}

	// The site's live version always survives, even beyond the window.
	old := model.Site{Id: "site-2", Version: 1}
	oldVersions := []model.ConfigVersion{
		{Id: "v1-live", SiteId: old.Id, Version: 1, Status: model.ConfigStatusPUBLISHED},
		{Id: "v2", SiteId: old.Id, Version: 2, Status: model.ConfigStatusFAILED},
		{Id: "v3", SiteId: old.Id, Version: 3, Status: model.ConfigStatusFAILED},
	}
	pruned = pruneConfigVersionIDs([]model.Site{old}, oldVersions, 1)
	// v3 is the newest terminal row and survives the window; v2 is superseded;
	// v1 survives both as the live version and the published baseline.
	if len(pruned) != 1 || pruned[0] != "v2" {
		t.Fatalf("pruned = %v, want [v2]", pruned)
	}

	// Orphaned snapshots without a site are deletable.
	orphan := []model.ConfigVersion{{Id: "orphan", SiteId: "missing", Version: 1}}
	if pruned = pruneConfigVersionIDs(nil, orphan, 5); len(pruned) != 1 || pruned[0] != "orphan" {
		t.Fatalf("orphan not pruned: %v", pruned)
	}

	// After compensation, live is a ROLLED_BACK copy. A later FAILED storm
	// may evict the original PUBLISHED row, but execute still has to restore
	// from the newest PUBLISHED or ROLLED_BACK snapshot below the failed version.
	rolled := model.Site{Id: "site-rollback", Version: 3}
	storm := []model.ConfigVersion{
		{Id: "pub-1", SiteId: rolled.Id, Version: 1, Status: model.ConfigStatusPUBLISHED},
		{Id: "fail-2", SiteId: rolled.Id, Version: 2, Status: model.ConfigStatusFAILED},
		{Id: "roll-3", SiteId: rolled.Id, Version: 3, Status: model.ConfigStatusROLLED_BACK},
		{Id: "fail-4", SiteId: rolled.Id, Version: 4, Status: model.ConfigStatusFAILED},
		{Id: "fail-5", SiteId: rolled.Id, Version: 5, Status: model.ConfigStatusFAILED},
		{Id: "fail-6", SiteId: rolled.Id, Version: 6, Status: model.ConfigStatusFAILED},
		{Id: "fail-7", SiteId: rolled.Id, Version: 7, Status: model.ConfigStatusFAILED},
		{Id: "draft-8", SiteId: rolled.Id, Version: 8, Status: model.ConfigStatusDRAFT},
	}
	pruned = pruneConfigVersionIDs([]model.Site{rolled}, storm, 3)
	remaining := remainingConfigVersions(storm, pruned)
	baseline := newestRestorableConfig(remaining, rolled.Id, 8)
	if baseline == nil || baseline.Id != "roll-3" {
		t.Fatalf("rollback baseline after prune = %#v, want roll-3 (pruned=%v)", baseline, pruned)
	}
}

func remainingConfigVersions(versions []model.ConfigVersion, prunedIDs []string) []model.ConfigVersion {
	drop := make(map[string]struct{}, len(prunedIDs))
	for _, id := range prunedIDs {
		drop[id] = struct{}{}
	}
	remaining := make([]model.ConfigVersion, 0, len(versions))
	for _, row := range versions {
		if _, dropped := drop[row.Id]; dropped {
			continue
		}
		remaining = append(remaining, row)
	}
	return remaining
}

func newestRestorableConfig(versions []model.ConfigVersion, siteID string, belowVersion int64) *model.ConfigVersion {
	var best *model.ConfigVersion
	for index := range versions {
		row := &versions[index]
		if row.SiteId != siteID || row.Version >= belowVersion || !restorableConfigStatus(row.Status) {
			continue
		}
		if best == nil || row.Version > best.Version {
			best = row
		}
	}
	return best
}

func TestNormalizeListenerWithoutCertificatesFallsBackToHTTP(t *testing.T) {
	config := edgeprotocol.SiteConfig{
		SiteID:  "site-http",
		Version: 1,
		Domains: []string{"example.test"},
		Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin.test:80"}},
		Listener: edgeprotocol.ListenerConfig{
			HTTPPort:              80,
			RedirectHTTPToHTTPS:   true,
			HTTPSEnabled:          true,
			HTTPSPort:             443,
			HTTP2Enabled:          true,
			HTTP3Enabled:          true,
			HSTSEnabled:           true,
			HSTSIncludeSubdomains: true,
			HSTSPreload:           true,
			OCSPStaplingEnabled:   true,
		},
	}

	normalizeListenerForCertificates(&config)

	if !config.Listener.HTTPEnabled || config.Listener.HTTPSEnabled || config.Listener.RedirectHTTPToHTTPS {
		t.Fatalf("expected HTTP-only listener, got %#v", config.Listener)
	}
	if config.Listener.HTTP2Enabled || config.Listener.HTTP3Enabled || config.Listener.HSTSEnabled || config.Listener.OCSPStaplingEnabled {
		t.Fatalf("HTTPS-only features remain enabled: %#v", config.Listener)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("HTTP fallback config should be accepted by the agent: %v", err)
	}
}

func TestNormalizeListenerWithCertificatePreservesHTTPS(t *testing.T) {
	config := edgeprotocol.SiteConfig{
		Listener:     edgeprotocol.ListenerConfig{HTTPSEnabled: true, RedirectHTTPToHTTPS: true},
		Certificates: []edgeprotocol.CertificateConfig{{CertificatePEM: "cert", PrivateKeyPEM: "key"}},
	}

	normalizeListenerForCertificates(&config)

	if !config.Listener.HTTPSEnabled || !config.Listener.RedirectHTTPToHTTPS {
		t.Fatalf("HTTPS listener was unexpectedly changed: %#v", config.Listener)
	}
}

func TestNewWithCiphersRequiresConfigSecretCipher(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil config secret cipher was accepted")
		}
	}()
	NewWithCiphers(nil, nil, nil, nil, nil)
}

func TestDecodeStoredConfigUnsealsAndRejectsForeignKeys(t *testing.T) {
	service := sealedSnapshotService(t)
	config := secretBearingSiteConfig()
	sealedJSON, err := service.sealedConfigJSON(config)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := service.decodeStoredConfig(sealedJSON)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Certificates[0].PrivateKeyPEM != "private-key-pem" || decoded.WAF["challenge_secret"] != "waf-secret" {
		t.Fatalf("unsealed snapshot missing secrets: %#v", decoded)
	}

	legacy, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = service.decodeStoredConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Certificates[0].PrivateKeyPEM != "private-key-pem" {
		t.Fatal("legacy plaintext snapshot was not accepted")
	}

	foreign := sealedSnapshotServiceWithKey(t, "publisher-seal-foreign")
	foreignJSON, err := foreign.sealedConfigJSON(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.decodeStoredConfig(foreignJSON); err == nil {
		t.Fatal("snapshot sealed with an unavailable key was accepted")
	}
}

func TestRollbackSnapshotTable(t *testing.T) {
	service := sealedSnapshotService(t)
	plain := secretBearingSiteConfig()
	plain.Version = 5
	legacyJSON, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	sealedJSON, err := service.sealedConfigJSON(plain)
	if err != nil {
		t.Fatal(err)
	}
	foreignJSON, err := sealedSnapshotServiceWithKey(t, "publisher-seal-foreign").sealedConfigJSON(plain)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name             string
		previous         *model.ConfigVersion
		wantDisabled     bool
		wantPlainSecrets bool
	}{
		{name: "first publish", wantDisabled: true},
		{
			name:             "legacy plaintext",
			previous:         &model.ConfigVersion{SiteId: "site-1", Version: 5, ConfigJson: legacyJSON},
			wantPlainSecrets: true,
		},
		{
			name:             "sealed previous",
			previous:         &model.ConfigVersion{SiteId: "site-1", Version: 5, ConfigJson: sealedJSON},
			wantPlainSecrets: true,
		},
		{
			name:         "unavailable key",
			previous:     &model.ConfigVersion{SiteId: "site-1", Version: 5, ConfigJson: foreignJSON},
			wantDisabled: true,
		},
		{
			name:         "unreadable json",
			previous:     &model.ConfigVersion{SiteId: "site-1", Version: 5, ConfigJson: []byte(`{`)},
			wantDisabled: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := service.rollbackSnapshot("site-1", 7, test.previous)
			if got.SiteID != "site-1" || got.Version != 7 {
				t.Fatalf("rollback identity = %s v%d", got.SiteID, got.Version)
			}
			if got.Disabled != test.wantDisabled {
				t.Fatalf("disabled = %v, want %v", got.Disabled, test.wantDisabled)
			}
			hasSecret := len(got.Certificates) > 0 && got.Certificates[0].PrivateKeyPEM == "private-key-pem"
			if hasSecret != test.wantPlainSecrets {
				t.Fatalf("plaintext secrets = %v, want %v: %#v", hasSecret, test.wantPlainSecrets, got)
			}
		})
	}
}

func TestPersistableRollbackJSONResealsUnderNewVersion(t *testing.T) {
	service := sealedSnapshotService(t)
	plain := secretBearingSiteConfig()
	plain.Version = 5
	sealedJSON, err := service.sealedConfigJSON(plain)
	if err != nil {
		t.Fatal(err)
	}
	rollback := service.rollbackSnapshot("site-1", 7, &model.ConfigVersion{
		SiteId: "site-1", Version: 5, ConfigJson: sealedJSON,
	})
	stored := service.persistableRollbackJSON(rollback)
	for _, secret := range []string{"private-key-pem", "mtls-private-key", "waf-secret"} {
		if strings.Contains(string(stored), secret) {
			t.Fatalf("secret %q persisted in rollback snapshot: %s", secret, stored)
		}
	}

	var persisted edgeprotocol.SiteConfig
	if err = json.Unmarshal(stored, &persisted); err != nil {
		t.Fatal(err)
	}
	foreign := persisted
	if err = service.configSecrets.UnsealSiteConfigSecrets("site-1", 5, &foreign); err == nil {
		t.Fatal("rollback remained sealed under the previous version")
	}
	if err = service.configSecrets.UnsealSiteConfigSecrets("site-1", 7, &persisted); err != nil {
		t.Fatalf("rollback was not sealed under the new version: %v", err)
	}
	if persisted.Certificates[0].PrivateKeyPEM != "private-key-pem" {
		t.Fatal("resealed rollback did not round-trip")
	}
}

func TestPersistableRollbackJSONFallsBackToTombstoneOnSealFailure(t *testing.T) {
	service := sealedSnapshotService(t)
	alreadySealed := secretBearingSiteConfig()
	alreadySealed.Version = 7
	if err := service.configSecrets.SealSiteConfigSecrets("site-1", 7, &alreadySealed); err != nil {
		t.Fatal(err)
	}
	stored := service.persistableRollbackJSON(alreadySealed)
	var persisted edgeprotocol.SiteConfig
	if err := json.Unmarshal(stored, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Disabled || persisted.Version != 7 || service.configSecrets.HasSecrets(&persisted) {
		t.Fatalf("seal failure did not persist a tombstone: %#v", persisted)
	}
}

func TestRewrapConfigVersionRowsSkipsCorruptRowsAndContinues(t *testing.T) {
	unavailable := errors.New("key unavailable")
	persisted := map[string][]byte{}
	result, err := rewrapConfigVersionRows(context.Background(), []model.ConfigVersion{
		{Id: "bad-json", SiteId: "site-1", Version: 1, ConfigJson: []byte(`{`)},
		{Id: "bad-key", SiteId: "site-2", Version: 2, ConfigJson: []byte(`{"site_id":"site-2","version":2}`)},
		{Id: "good", SiteId: "site-3", Version: 3, ConfigJson: []byte(`{"site_id":"site-3","version":3}`)},
	}, func(siteID string, _ uint64, config *edgeprotocol.SiteConfig) (bool, error) {
		if siteID == "site-2" {
			return false, unavailable
		}
		config.Domains = []string{"rewrapped.test"}
		return true, nil
	}, func(id string, encoded []byte) error {
		persisted[id] = encoded
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 2 || result.Skipped[0].VersionID != "bad-json" ||
		result.Skipped[1].VersionID != "bad-key" || !errors.Is(result.Skipped[1].Err, unavailable) {
		t.Fatalf("unexpected skipped snapshots: %#v", result.Skipped)
	}
	if _, ok := persisted["good"]; !ok {
		t.Fatalf("valid snapshot was not rewrapped after corrupt rows: %#v", persisted)
	}
	if _, ok := persisted["bad-json"]; ok || persisted["bad-key"] != nil {
		t.Fatalf("corrupt snapshots were persisted: %#v", persisted)
	}
}

func TestRewrapConfigVersionRowsReturnsPersistenceErrors(t *testing.T) {
	persistErr := errors.New("database unavailable")
	_, err := rewrapConfigVersionRows(context.Background(), []model.ConfigVersion{
		{Id: "good", SiteId: "site-1", Version: 1, ConfigJson: []byte(`{"site_id":"site-1","version":1}`)},
	}, func(string, uint64, *edgeprotocol.SiteConfig) (bool, error) {
		return true, nil
	}, func(string, []byte) error {
		return persistErr
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("rewrap error = %v, want %v", err, persistErr)
	}
}

func TestRewrapConfigVersionRowsSkipsRemainingOnTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	persisted := map[string][]byte{}
	result, err := rewrapConfigVersionRows(ctx, []model.ConfigVersion{
		{Id: "first", SiteId: "site-1", Version: 1, ConfigJson: []byte(`{"site_id":"site-1","version":1}`)},
		{Id: "second", SiteId: "site-2", Version: 2, ConfigJson: []byte(`{"site_id":"site-2","version":2}`)},
		{Id: "third", SiteId: "site-3", Version: 3, ConfigJson: []byte(`{"site_id":"site-3","version":3}`)},
	}, func(string, uint64, *edgeprotocol.SiteConfig) (bool, error) {
		return true, nil
	}, func(id string, encoded []byte) error {
		persisted[id] = encoded
		if id == "first" {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := persisted["first"]; !ok {
		t.Fatal("first snapshot was not rewrapped before timeout")
	}
	if len(result.Skipped) != 2 || result.Skipped[0].VersionID != "second" || result.Skipped[1].VersionID != "third" {
		t.Fatalf("timeout skipped = %#v, want second and third", result.Skipped)
	}
	for _, failure := range result.Skipped {
		if !errors.Is(failure.Err, context.Canceled) {
			t.Fatalf("skipped error = %v, want context canceled", failure.Err)
		}
	}
	if _, ok := persisted["second"]; ok || persisted["third"] != nil {
		t.Fatalf("snapshots after timeout were persisted: %#v", persisted)
	}
}

func TestRewrapConfigVersionRowsSkipsOnPersistTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := rewrapConfigVersionRows(ctx, []model.ConfigVersion{
		{Id: "good", SiteId: "site-1", Version: 1, ConfigJson: []byte(`{"site_id":"site-1","version":1}`)},
		{Id: "later", SiteId: "site-2", Version: 2, ConfigJson: []byte(`{"site_id":"site-2","version":2}`)},
	}, func(string, uint64, *edgeprotocol.SiteConfig) (bool, error) {
		return true, nil
	}, func(string, []byte) error {
		cancel()
		return context.Canceled
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 2 || result.Skipped[0].VersionID != "good" || result.Skipped[1].VersionID != "later" {
		t.Fatalf("persist timeout skipped = %#v, want good and later", result.Skipped)
	}
	for _, failure := range result.Skipped {
		if !errors.Is(failure.Err, context.Canceled) {
			t.Fatalf("skipped error = %v, want context canceled", failure.Err)
		}
	}
}
