package edgeagent

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

func newAgentServer(identity Identity, configs *ConfigManager, nodeConfigs *NodeConfigStore, logs *LogQueue) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sites/{site_id}/config", func(w http.ResponseWriter, r *http.Request) {
		var config SiteConfig
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&config); err != nil {
			writeError(w, http.StatusBadRequest, "invalid config payload")
			return
		}
		if config.SiteID != r.PathValue("site_id") {
			writeError(w, http.StatusBadRequest, "site id mismatch")
			return
		}
		if err := configs.ApplySite(config); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"site_id": config.SiteID, "version": config.Version, "applied": true})
	})
	mux.HandleFunc("GET /v1/logs/pull", func(w http.ResponseWriter, r *http.Request) {
		wait := parseDurationSeconds(r.URL.Query().Get("wait"), 45*time.Second, 60*time.Second)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit < 1 || limit > 5000 {
			limit = 1000
		}
		batch, err := logs.Batch(limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read log queue")
			return
		}
		if len(batch) == 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-logs.Wait():
			case <-timer.C:
			}
			batch, err = logs.Batch(limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "read log queue")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"node_id": identity.NodeID, "records": batch})
	})
	mux.HandleFunc("POST /v1/logs/ack", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Through uint64 `json:"through"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Through == 0 {
			writeError(w, http.StatusBadRequest, "invalid ack")
			return
		}
		if err := logs.Ack(input.Through); err != nil {
			writeError(w, http.StatusInternalServerError, "ack log queue")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /v1/node/config", func(w http.ResponseWriter, r *http.Request) {
		var config NodeConfig
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			writeError(w, http.StatusBadRequest, "invalid node config")
			return
		}
		if err := nodeConfigs.Set(config); err != nil {
			writeError(w, http.StatusInternalServerError, "persist node config")
			return
		}
		writeJSON(w, http.StatusOK, nodeConfigs.Get())
	})
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"node_id": identity.NodeID, "status": "ok"})
	})
	return newAuthenticator(identity).wrap(mux)
}

type authenticator struct {
	identity Identity
	mu       sync.Mutex
	nonces   map[string]time.Time
}

func newAuthenticator(identity Identity) *authenticator {
	return &authenticator{identity: identity, nonces: map[string]time.Time{}}
}

func (a *authenticator) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if parsedHost, _, err := net.SplitHostPort(r.Host); err == nil {
			host = parsedHost
		}
		timestampText, nonce := r.Header.Get(edgeprotocol.HeaderTimestamp), r.Header.Get(edgeprotocol.HeaderNonce)
		timestamp, err := time.Parse(time.RFC3339, timestampText)
		if err != nil || time.Since(timestamp) > 5*time.Minute || time.Until(timestamp) > 5*time.Minute || nonce == "" {
			writeError(w, http.StatusUnauthorized, "invalid or replayed request signature")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "request body too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		contentHash := edgeprotocol.ContentHash(body)
		if host != a.identity.NodeID || r.Header.Get(edgeprotocol.HeaderNodeID) != a.identity.NodeID || r.Header.Get(edgeprotocol.HeaderContentHash) != contentHash || !edgeprotocol.Verify(a.identity.CommunicationKey, r.Header.Get(edgeprotocol.HeaderSignature), r.Method, host, r.URL.RequestURI(), timestampText, nonce, contentHash) || !a.acceptNonce(nonce, time.Now()) {
			writeError(w, http.StatusUnauthorized, "invalid node credentials")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *authenticator) acceptNonce(nonce string, now time.Time) bool {
	if nonce == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for value, expires := range a.nonces {
		if now.After(expires) {
			delete(a.nonces, value)
		}
	}
	if _, exists := a.nonces[nonce]; exists {
		return false
	}
	a.nonces[nonce] = now.Add(5 * time.Minute)
	return true
}

func parseDurationSeconds(value string, fallback, maximum time.Duration) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 1 {
		return fallback
	}
	result := time.Duration(seconds) * time.Second
	if result > maximum {
		return maximum
	}
	return result
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
