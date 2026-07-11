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

func TestSignatureIsCaseNormalized(t *testing.T) {
	key := "secret"
	signature := Sign(key, "put", "NODE-ID", "/v1/x", "ts", "n", ContentHash(nil))
	if !Verify(key, signature, "PUT", "node-id", "/v1/x", "ts", "n", ContentHash(nil)) {
		t.Fatal("case-normalized signature should verify")
	}
}

func TestVerifyRejectsMalformedSignature(t *testing.T) {
	if Verify("secret", "not-hex", "GET", "host", "/", "ts", "n", ContentHash(nil)) {
		t.Fatal("malformed signature accepted")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	signature := Sign("secret", "GET", "host", "/", "ts", "n", ContentHash(nil))
	if Verify("other", signature, "GET", "host", "/", "ts", "n", ContentHash(nil)) {
		t.Fatal("wrong key accepted")
	}
}

func TestContentHashStable(t *testing.T) {
	if ContentHash([]byte("a")) == ContentHash([]byte("b")) {
		t.Fatal("different bodies produced same hash")
	}
	if ContentHash(nil) != ContentHash([]byte{}) {
		t.Fatal("nil and empty body should hash the same")
	}
}
