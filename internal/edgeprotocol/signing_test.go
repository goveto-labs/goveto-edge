package edgeprotocol

import "testing"

func TestSignatureCoversRequest(t *testing.T) {
	key := "secret"
	signature := Sign(key, "POST", "node-id", "/v1/logs/ack", "2026-07-11T10:00:00Z", "nonce", ContentHash([]byte(`{"through":1}`)))
	if !Verify(key, signature, "POST", "node-id", "/v1/logs/ack", "2026-07-11T10:00:00Z", "nonce", ContentHash([]byte(`{"through":1}`))) {
		t.Fatal("valid signature rejected")
	}
	if Verify(key, signature, "POST", "node-id", "/v1/logs/ack", "2026-07-11T10:00:00Z", "nonce", ContentHash([]byte(`{"through":2}`))) {
		t.Fatal("modified body accepted")
	}
}
