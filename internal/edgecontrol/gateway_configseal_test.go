package edgecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"goveto-edge/internal/configseal"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
)

func testConfigSealer(t *testing.T) *configseal.Sealer {
	t.Helper()
	key := sha256.Sum256([]byte("gateway-configseal-test"))
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(key[:]))
	if err != nil {
		t.Fatal(err)
	}
	return configseal.New(cipher)
}

func secretBearingApplyPayload() edgeprotocol.SiteConfig {
	config := edgeprotocol.SiteConfig{
		SiteID: "site-1", Version: 9, Domains: []string{"example.test"},
		WAF: map[string]any{"challenge_secret": "waf-secret"},
	}
	config.Certificates = []edgeprotocol.CertificateConfig{{CertificatePEM: "certificate-pem", PrivateKeyPEM: "private-key-pem"}}
	config.OriginPolicy.Transport.TLSClientPrivateKeyPEM = "mtls-private-key"
	return config
}

func TestPersistableApplySiteConfigPayloadHidesSecrets(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	gateway.ConfigureConfigSealer(testConfigSealer(t))
	plain, err := json.Marshal(secretBearingApplyPayload())
	if err != nil {
		t.Fatal(err)
	}

	stored, err := gateway.persistableApplySiteConfigPayload(edgeprotocol.TaskApplySiteConfig, plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-key-pem", "mtls-private-key", "waf-secret"} {
		if strings.Contains(string(stored), secret) {
			t.Fatalf("secret %q persisted in agent task payload: %s", secret, stored)
		}
	}

	delivered, err := gateway.deliverableApplySiteConfigPayload(edgeprotocol.TaskApplySiteConfig, stored)
	if err != nil {
		t.Fatal(err)
	}
	var config edgeprotocol.SiteConfig
	if err = json.Unmarshal(delivered, &config); err != nil {
		t.Fatal(err)
	}
	if config.Certificates[0].PrivateKeyPEM != "private-key-pem" || config.WAF["challenge_secret"] != "waf-secret" {
		t.Fatalf("claimed payload was not unsealed: %#v", config)
	}
}

func TestPersistableApplySiteConfigPayloadRejectsSecretsWithoutSealer(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	plain, err := json.Marshal(secretBearingApplyPayload())
	if err != nil {
		t.Fatal(err)
	}
	_, err = gateway.persistableApplySiteConfigPayload(edgeprotocol.TaskApplySiteConfig, plain)
	if !errors.Is(err, configseal.ErrSealerUnavailable) {
		t.Fatalf("secret-bearing payload without sealer: %v", err)
	}

	tombstone, err := json.Marshal(edgeprotocol.SiteConfig{SiteID: "site-1", Version: 2, Disabled: true})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := gateway.persistableApplySiteConfigPayload(edgeprotocol.TaskApplySiteConfig, tombstone)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(tombstone) {
		t.Fatal("secret-free payload was rewritten without a sealer")
	}
}

func TestPersistableApplySiteConfigPayloadLeavesOtherKindsAlone(t *testing.T) {
	gateway := NewGateway(nil, nil, nil, nil, nil)
	payload := []byte(`{"sha256":"abc"}`)
	stored, err := gateway.persistableApplySiteConfigPayload(edgeprotocol.TaskSyncGeoIP, payload)
	if err != nil || string(stored) != string(payload) {
		t.Fatalf("non-config task payload was rewritten: %s err=%v", stored, err)
	}
}

func TestRewrapApplySiteConfigPayloadUsesCurrentKey(t *testing.T) {
	previousKey := base64.StdEncoding.EncodeToString(sha256Sum("gateway-previous"))
	currentKey := base64.StdEncoding.EncodeToString(sha256Sum("gateway-current"))
	previousCipher, err := node.NewCredentialCipher(previousKey)
	if err != nil {
		t.Fatal(err)
	}
	currentCipher, err := node.NewCredentialCipherKeyring(currentKey, previousKey)
	if err != nil {
		t.Fatal(err)
	}

	config := secretBearingApplyPayload()
	if err = configseal.New(previousCipher).SealSiteConfigSecrets(config.SiteID, config.Version, &config); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}

	gateway := NewGateway(nil, nil, nil, nil, nil)
	gateway.ConfigureConfigSealer(configseal.New(currentCipher))
	rewrapped, changed, err := gateway.rewrapApplySiteConfigPayload(encoded)
	if err != nil || !changed {
		t.Fatalf("rewrap changed=%v err=%v", changed, err)
	}

	onlyCurrent, err := node.NewCredentialCipher(currentKey)
	if err != nil {
		t.Fatal(err)
	}
	currentOnly := NewGateway(nil, nil, nil, nil, nil)
	currentOnly.ConfigureConfigSealer(configseal.New(onlyCurrent))
	delivered, err := currentOnly.deliverableApplySiteConfigPayload(edgeprotocol.TaskApplySiteConfig, rewrapped)
	if err != nil {
		t.Fatalf("rewrapped payload still requires previous key: %v", err)
	}
	var got edgeprotocol.SiteConfig
	if err = json.Unmarshal(delivered, &got); err != nil {
		t.Fatal(err)
	}
	if got.Certificates[0].PrivateKeyPEM != "private-key-pem" {
		t.Fatal("rewrapped payload did not round-trip")
	}
}

func TestRewrapApplySiteConfigRowsSkipsCorruptTasksAndContinues(t *testing.T) {
	unavailable := errors.New("key unavailable")
	persisted := map[string][]byte{}
	result, err := rewrapApplySiteConfigRows(context.Background(), []applySiteConfigTaskRow{
		{ID: "bad-task", Payload: []byte(`{"site_id":"site-1"}`)},
		{ID: "good-task", Payload: []byte(`{"site_id":"site-2"}`)},
	}, func(payload []byte) ([]byte, bool, error) {
		var config edgeprotocol.SiteConfig
		if err := json.Unmarshal(payload, &config); err != nil {
			return nil, false, err
		}
		if config.SiteID == "site-1" {
			return nil, false, unavailable
		}
		return []byte(`{"site_id":"site-2","rewrapped":true}`), true, nil
	}, func(id string, encoded []byte) error {
		persisted[id] = encoded
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].TaskID != "bad-task" ||
		!errors.Is(result.Skipped[0].Err, unavailable) {
		t.Fatalf("unexpected skipped tasks: %#v", result.Skipped)
	}
	if _, ok := persisted["good-task"]; !ok {
		t.Fatalf("valid task was not rewrapped after corrupt row: %#v", persisted)
	}
}

func TestRewrapApplySiteConfigRowsReturnsPersistenceErrors(t *testing.T) {
	persistErr := errors.New("database unavailable")
	_, err := rewrapApplySiteConfigRows(context.Background(), []applySiteConfigTaskRow{
		{ID: "task-1", Payload: []byte(`{"site_id":"site-1"}`)},
	}, func([]byte) ([]byte, bool, error) {
		return []byte(`{"rewrapped":true}`), true, nil
	}, func(string, []byte) error {
		return persistErr
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("rewrap error = %v, want %v", err, persistErr)
	}
}

func TestRewrapApplySiteConfigRowsSkipsRemainingOnTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	persisted := map[string][]byte{}
	result, err := rewrapApplySiteConfigRows(ctx, []applySiteConfigTaskRow{
		{ID: "first", Payload: []byte(`{"site_id":"site-1"}`)},
		{ID: "second", Payload: []byte(`{"site_id":"site-2"}`)},
		{ID: "third", Payload: []byte(`{"site_id":"site-3"}`)},
	}, func(payload []byte) ([]byte, bool, error) {
		return payload, true, nil
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
		t.Fatal("first task was not rewrapped before timeout")
	}
	if len(result.Skipped) != 2 || result.Skipped[0].TaskID != "second" || result.Skipped[1].TaskID != "third" {
		t.Fatalf("timeout skipped = %#v, want second and third", result.Skipped)
	}
	if _, ok := persisted["second"]; ok || persisted["third"] != nil {
		t.Fatalf("tasks after timeout were persisted: %#v", persisted)
	}
}

func TestRewrapApplySiteConfigRowsSkipsOnPersistTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := rewrapApplySiteConfigRows(ctx, []applySiteConfigTaskRow{
		{ID: "first", Payload: []byte(`{"site_id":"site-1"}`)},
		{ID: "later", Payload: []byte(`{"site_id":"site-2"}`)},
	}, func(payload []byte) ([]byte, bool, error) {
		return payload, true, nil
	}, func(string, []byte) error {
		cancel()
		return context.Canceled
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 2 || result.Skipped[0].TaskID != "first" || result.Skipped[1].TaskID != "later" {
		t.Fatalf("persist timeout skipped = %#v, want first and later", result.Skipped)
	}
}

func sha256Sum(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
