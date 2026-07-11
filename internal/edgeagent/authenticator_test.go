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
	handler := newAuthenticator(identity).wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
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
