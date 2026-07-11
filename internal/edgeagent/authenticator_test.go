package edgeagent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

func TestAuthenticatorRejectsReplay(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	path := t.TempDir() + "/nonces.json"
	handler := newAuthenticator(identity, path).wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := signedTestRequest(identity, "nonce-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("signed request rejected: %d", response.Code)
	}
	replay := signedTestRequest(identity, "nonce-1")
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replay accepted: %d", replayResponse.Code)
	}
	restarted := newAuthenticator(identity, path).wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	restartReplay := signedTestRequest(identity, "nonce-1")
	restartResponse := httptest.NewRecorder()
	restarted.ServeHTTP(restartResponse, restartReplay)
	if restartResponse.Code != http.StatusUnauthorized {
		t.Fatalf("persisted replay accepted: %d", restartResponse.Code)
	}
}

func TestHealthBypassesAuthentication(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	handler := newAuthenticator(identity, "").wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://node/v1/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health should be unauthenticated: %d", response.Code)
	}
}

func TestAuthenticatorAcceptsHostWithPort(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	handler := newAuthenticator(identity, "").wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := signedTestRequest(identity, "nonce-port")
	request.Host = identity.NodeID + ":8443"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("host with port rejected: %d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthenticatorRejectsBadSignature(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	handler := newAuthenticator(identity, "").wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := signedTestRequest(identity, "nonce-bad")
	request.Header.Set(edgeprotocol.HeaderSignature, strings.Repeat("0", 64))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature accepted: %d", response.Code)
	}
}

func TestAuthenticatorRejectsExpiredTimestamp(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	handler := newAuthenticator(identity, "").wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	body := []byte(`{"value":1}`)
	request := httptest.NewRequest(http.MethodPut, "http://node/v1/node/config", strings.NewReader(string(body)))
	request.Host = identity.NodeID
	timestamp := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	hash := edgeprotocol.ContentHash(body)
	request.Header.Set(edgeprotocol.HeaderNodeID, identity.NodeID)
	request.Header.Set(edgeprotocol.HeaderTimestamp, timestamp)
	request.Header.Set(edgeprotocol.HeaderNonce, "nonce-expired")
	request.Header.Set(edgeprotocol.HeaderContentHash, hash)
	request.Header.Set(edgeprotocol.HeaderSignature, edgeprotocol.Sign(identity.CommunicationKey, request.Method, identity.NodeID, request.URL.RequestURI(), timestamp, "nonce-expired", hash))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired timestamp accepted: %d", response.Code)
	}
}

func TestAuthenticatorRejectsWrongNodeID(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	handler := newAuthenticator(identity, "").wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := signedTestRequest(identity, "nonce-node")
	request.Header.Set(edgeprotocol.HeaderNodeID, "00000000-0000-0000-0000-000000000000")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong node id accepted: %d", response.Code)
	}
}

func signedTestRequest(identity Identity, nonce string) *http.Request {
	body := []byte(`{"value":1}`)
	request := httptest.NewRequest(http.MethodPut, "http://node/v1/node/config", strings.NewReader(string(body)))
	request.Host = identity.NodeID
	timestamp := time.Now().UTC().Format(time.RFC3339)
	hash := edgeprotocol.ContentHash(body)
	request.Header.Set(edgeprotocol.HeaderNodeID, identity.NodeID)
	request.Header.Set(edgeprotocol.HeaderTimestamp, timestamp)
	request.Header.Set(edgeprotocol.HeaderNonce, nonce)
	request.Header.Set(edgeprotocol.HeaderContentHash, hash)
	request.Header.Set(edgeprotocol.HeaderSignature, edgeprotocol.Sign(identity.CommunicationKey, request.Method, identity.NodeID, request.URL.RequestURI(), timestamp, nonce, hash))
	return request
}
