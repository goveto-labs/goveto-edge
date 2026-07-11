package edgeprotocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	HeaderNodeID      = "X-Goveto-Node-ID"
	HeaderTimestamp   = "X-Goveto-Timestamp"
	HeaderNonce       = "X-Goveto-Nonce"
	HeaderContentHash = "X-Goveto-Content-SHA256"
	HeaderSignature   = "X-Goveto-Signature"
)

func ContentHash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func Sign(key, method, host, requestURI, timestamp, nonce, contentHash string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(canonical(method, host, requestURI, timestamp, nonce, contentHash)))
	return hex.EncodeToString(mac.Sum(nil))
}

func Verify(key, signature, method, host, requestURI, timestamp, nonce, contentHash string) bool {
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(Sign(key, method, host, requestURI, timestamp, nonce, contentHash))
	if err != nil {
		return false
	}
	return hmac.Equal(provided, expected)
}

func canonical(method, host, requestURI, timestamp, nonce, contentHash string) string {
	return strings.Join([]string{strings.ToUpper(method), strings.ToLower(host), requestURI, timestamp, nonce, contentHash}, "\n")
}
