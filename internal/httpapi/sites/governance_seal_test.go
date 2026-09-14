package sites

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
	"goveto-edge/internal/storage/gen/model"
)

func testGovernanceSealer(t *testing.T) *configseal.Sealer {
	t.Helper()
	key := sha256.Sum256([]byte("sites-governance-test"))
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(key[:]))
	if err != nil {
		t.Fatal(err)
	}
	return configseal.New(cipher)
}

func TestSealedGovernanceJSONRoundTrip(t *testing.T) {
	sealer := testGovernanceSealer(t)
	policy := edgeprotocol.DefaultOriginPolicy()
	policy.Transport.TLSClientCertificatePEM = "mtls-certificate"
	policy.Transport.TLSClientPrivateKeyPEM = "mtls-private-key"

	encoded, err := sealedGovernanceJSON(sealer, "cluster-1", "pool-1", policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "mtls-private-key") {
		t.Fatalf("governance snapshot leaks the mTLS key: %s", encoded)
	}
	if !strings.Contains(string(encoded), "mtls-certificate") {
		t.Fatalf("public mTLS certificate missing: %s", encoded)
	}

	parsed, err := unmarshalGovernancePolicy(sealer, "cluster-1", "pool-1", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Transport.TLSClientPrivateKeyPEM != "mtls-private-key" {
		t.Fatal("sealed governance did not round-trip")
	}

	// Legacy plaintext governance rows keep parsing without a sealer.
	legacy, err := json.Marshal(edgeprotocol.DefaultOriginPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = unmarshalGovernancePolicy(nil, "cluster-1", "pool-1", legacy); err != nil {
		t.Fatalf("legacy plaintext governance rejected: %v", err)
	}

	// A nil sealer passes the envelope through; every response path redacts
	// the mTLS key, and writes fail closed instead of double sealing.
	if _, err = unmarshalGovernancePolicy(nil, "cluster-1", "pool-1", encoded); err != nil {
		t.Fatalf("sealed governance without sealer: %v", err)
	}
}

func TestSealedGovernanceJSONWithoutSecrets(t *testing.T) {
	encoded, err := sealedGovernanceJSON(testGovernanceSealer(t), "cluster-1", "pool-1", edgeprotocol.DefaultOriginPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "enc:v2:") {
		t.Fatalf("secret-free governance carries an envelope: %s", encoded)
	}

	// A nil sealer is acceptable while no secrets need sealing...
	if _, err = sealedGovernanceJSON(nil, "cluster-1", "pool-1", edgeprotocol.DefaultOriginPolicy()); err != nil {
		t.Fatalf("secret-free governance with nil sealer rejected: %v", err)
	}
	// ...but mTLS keys fail closed instead of persisting plaintext.
	secretPolicy := edgeprotocol.DefaultOriginPolicy()
	secretPolicy.Transport.TLSClientPrivateKeyPEM = "mtls-private-key"
	if _, err = sealedGovernanceJSON(nil, "cluster-1", "pool-1", secretPolicy); err == nil {
		t.Fatal("mTLS key persisted without a sealer")
	}
	if !errors.Is(err, configseal.ErrSealerUnavailable) {
		t.Fatalf("seal error = %v, want ErrSealerUnavailable", err)
	}
}

func TestSealedGovernanceJSONResealsForClusterTransfer(t *testing.T) {
	sealer := testGovernanceSealer(t)
	policy := edgeprotocol.DefaultOriginPolicy()
	policy.Transport.TLSClientCertificatePEM = "mtls-certificate"
	policy.Transport.TLSClientPrivateKeyPEM = "mtls-private-key"

	encoded, err := sealedGovernanceJSON(sealer, "cluster-1", "pool-1", policy)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := unmarshalGovernancePolicy(sealer, "cluster-1", "pool-1", encoded)
	if err != nil {
		t.Fatal(err)
	}

	// Cluster transfer reseals the already-unsealed policy under the new
	// cluster id even when the PATCH omitted origin_policy.
	resealed, err := sealedGovernanceJSON(sealer, "cluster-2", "pool-1", plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = unmarshalGovernancePolicy(sealer, "cluster-1", "pool-1", resealed); err == nil {
		t.Fatal("ciphertext remained bound to the old cluster")
	}
	roundTrip, err := unmarshalGovernancePolicy(sealer, "cluster-2", "pool-1", resealed)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.Transport.TLSClientPrivateKeyPEM != "mtls-private-key" {
		t.Fatal("resealed governance did not round-trip under the new cluster")
	}
}

func TestRewrapOriginGovernanceRowsSkipsCorruptAndContinues(t *testing.T) {
	unavailable := errors.New("key unavailable")
	persisted := map[string][]byte{}
	result, err := rewrapOriginGovernanceRows(context.Background(), []model.OriginPool{
		{Id: "bad", ClusterId: "cluster-1", Governance: []byte(`{`)},
		{Id: "good", ClusterId: "cluster-1", Governance: []byte(`{}`)},
	}, func(clusterID, poolID string, policy *edgeprotocol.OriginPolicyConfig) (bool, error) {
		if poolID == "good" {
			return true, nil
		}
		return false, unavailable
	}, func(id string, encoded []byte) error {
		persisted[id] = encoded
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].PoolID != "bad" {
		t.Fatalf("unexpected skipped pools: %#v", result.Skipped)
	}
	if _, ok := persisted["good"]; !ok {
		t.Fatalf("valid pool was not rewrapped after corrupt row: %#v", persisted)
	}
}

func TestRewrapOriginGovernanceRowsReturnsPersistenceErrors(t *testing.T) {
	persistErr := errors.New("database unavailable")
	_, err := rewrapOriginGovernanceRows(context.Background(), []model.OriginPool{
		{Id: "pool-1", ClusterId: "cluster-1", Governance: []byte(`{}`)},
	}, func(string, string, *edgeprotocol.OriginPolicyConfig) (bool, error) {
		return true, nil
	}, func(string, []byte) error {
		return persistErr
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("rewrap error = %v, want %v", err, persistErr)
	}
}

func TestRewrapOriginGovernanceRowsSkipsRemainingOnTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	persisted := map[string][]byte{}
	result, err := rewrapOriginGovernanceRows(ctx, []model.OriginPool{
		{Id: "first", ClusterId: "cluster-1", Governance: []byte(`{}`)},
		{Id: "second", ClusterId: "cluster-1", Governance: []byte(`{}`)},
		{Id: "third", ClusterId: "cluster-1", Governance: []byte(`{}`)},
	}, func(string, string, *edgeprotocol.OriginPolicyConfig) (bool, error) {
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
		t.Fatal("first pool was not rewrapped before timeout")
	}
	if len(result.Skipped) != 2 || result.Skipped[0].PoolID != "second" || result.Skipped[1].PoolID != "third" {
		t.Fatalf("timeout skipped = %#v, want second and third", result.Skipped)
	}
	if _, ok := persisted["second"]; ok || persisted["third"] != nil {
		t.Fatalf("pools after timeout were persisted: %#v", persisted)
	}
}

func TestRewrapOriginGovernanceRowsSkipsOnPersistTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := rewrapOriginGovernanceRows(ctx, []model.OriginPool{
		{Id: "first", ClusterId: "cluster-1", Governance: []byte(`{}`)},
		{Id: "later", ClusterId: "cluster-1", Governance: []byte(`{}`)},
	}, func(string, string, *edgeprotocol.OriginPolicyConfig) (bool, error) {
		return true, nil
	}, func(string, []byte) error {
		cancel()
		return context.Canceled
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 2 || result.Skipped[0].PoolID != "first" || result.Skipped[1].PoolID != "later" {
		t.Fatalf("persist timeout skipped = %#v, want first and later", result.Skipped)
	}
}
