package certmanager

import (
	"context"
	"crypto"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRevokeACMECertificateSendsReasonAndAcceptsAlreadyRevoked(t *testing.T) {
	certificatePEM, privateKeyPEM := testCertificate(t, time.Now().UTC(), []string{"revoke.example.com"}, nil)
	pair, err := tls.X509KeyPair([]byte(certificatePEM), []byte(privateKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ParsePEMCertificates(certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)

	for _, alreadyRevoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "already-revoked"}[alreadyRevoked], func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Replay-Nonce", "test-nonce")
				switch request.URL.Path {
				case "/directory":
					response.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(response).Encode(map[string]string{
						"newNonce": server.URL + "/nonce", "newAccount": server.URL + "/account",
						"newOrder": server.URL + "/order", "revokeCert": server.URL + "/revoke",
					})
				case "/nonce":
					response.WriteHeader(http.StatusNoContent)
				case "/revoke":
					var message struct {
						Payload string `json:"payload"`
					}
					if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
						t.Error(err)
						response.WriteHeader(http.StatusBadRequest)
						return
					}
					payload, err := base64.RawURLEncoding.DecodeString(message.Payload)
					if err != nil {
						t.Error(err)
					}
					var body struct {
						Certificate string `json:"certificate"`
						Reason      int    `json:"reason"`
					}
					if err = json.Unmarshal(payload, &body); err != nil {
						t.Error(err)
					}
					if body.Reason != 1 || body.Certificate == "" {
						t.Errorf("unexpected revocation body: %#v", body)
					}
					if alreadyRevoked {
						response.Header().Set("Content-Type", "application/problem+json")
						response.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(response).Encode(map[string]any{
							"type": "urn:ietf:params:acme:error:alreadyRevoked", "status": 400,
						})
						return
					}
					response.WriteHeader(http.StatusOK)
				default:
					response.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			if err := revokeACMECertificate(context.Background(), server.URL+"/directory", leaf[0], signer, 1); err != nil {
				t.Fatal(err)
			}
		})
	}
}
