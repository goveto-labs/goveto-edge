// Package edgecontrol implements control-plane communication with edge agents.
package edgecontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

type Client struct {
	baseURL, nodeID, key string
	http                 *http.Client
}

func New(baseURL, nodeID, communicationKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		nodeID:  nodeID,
		key:     communicationKey,
		http:    &http.Client{Timeout: 75 * time.Second},
	}
}

type ApplySiteResult struct {
	SiteID        string `json:"site_id"`
	Version       uint64 `json:"version"`
	ConfigVersion uint64 `json:"config_version"`
	Applied       bool   `json:"applied"`
}

func (c *Client) PushSiteConfig(ctx context.Context, config edgeprotocol.SiteConfig) (ApplySiteResult, error) {
	body, err := json.Marshal(config)
	if err != nil {
		return ApplySiteResult{}, err
	}

	request, err := c.request(ctx, http.MethodPut, "/v1/sites/"+config.SiteID+"/config", body)
	if err != nil {
		return ApplySiteResult{}, err
	}

	response, err := c.http.Do(request)
	if err != nil {
		return ApplySiteResult{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return ApplySiteResult{}, fmt.Errorf("agent rejected site config: %s", response.Status)
	}

	var result ApplySiteResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return ApplySiteResult{}, fmt.Errorf("decode apply site response: %w", err)
	}
	return result, nil
}

func (c *Client) PurgeSite(ctx context.Context, purge edgeprotocol.PurgeRequest) error {
	body, err := json.Marshal(purge)
	if err != nil {
		return err
	}

	request, err := c.request(ctx, http.MethodPost, "/v1/sites/"+purge.SiteID+"/purge", body)
	if err != nil {
		return err
	}

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("agent rejected cache purge: %s", response.Status)
	}
	return nil
}

func (c *Client) PushNodeCacheConfig(ctx context.Context, config edgeprotocol.NodeCacheConfig) error {
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}

	request, err := c.request(ctx, http.MethodPut, "/v1/node/config", body)
	if err != nil {
		return err
	}

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("agent rejected node cache config: %s", response.Status)
	}
	return nil
}

// PullLogs continuously long-polls the agent. Records are acknowledged only
// after consume returns successfully, so transient control-plane failures do
// not lose data.
func (c *Client) PullLogs(ctx context.Context, consume func(context.Context, []edgeprotocol.LogRecord) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		request, err := c.request(ctx, http.MethodGet, "/v1/logs/pull?wait=60&limit=2000", nil)
		if err != nil {
			return err
		}

		response, err := c.http.Do(request)
		if err != nil {
			if err := retryDelay(ctx); err != nil {
				return err
			}
			continue
		}

		var payload struct {
			Records []edgeprotocol.LogRecord `json:"records"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()

		if response.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("pull agent logs: unauthorized")
		}
		if response.StatusCode != http.StatusOK || decodeErr != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := retryDelay(ctx); err != nil {
				return err
			}
			continue
		}

		if len(payload.Records) == 0 {
			continue
		}
		if err := consume(ctx, payload.Records); err != nil {
			return err
		}
		if err := c.ack(ctx, payload.Records[len(payload.Records)-1].ID); err != nil {
			return err
		}
	}
}

func retryDelay(ctx context.Context) error {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) ack(ctx context.Context, through uint64) error {
	body, _ := json.Marshal(map[string]uint64{"through": through})
	request, err := c.request(ctx, http.MethodPost, "/v1/logs/ack", body)
	if err != nil {
		return err
	}

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("ack agent logs: %s", response.Status)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	request.Host = c.nodeID
	timestamp := time.Now().UTC().Format(time.RFC3339)
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}

	contentHash := edgeprotocol.ContentHash(body)
	request.Header.Set(edgeprotocol.HeaderNodeID, c.nodeID)
	request.Header.Set(edgeprotocol.HeaderTimestamp, timestamp)
	request.Header.Set(edgeprotocol.HeaderNonce, nonce)
	request.Header.Set(edgeprotocol.HeaderContentHash, contentHash)
	request.Header.Set(
		edgeprotocol.HeaderSignature,
		edgeprotocol.Sign(c.key, method, c.nodeID, request.URL.RequestURI(), timestamp, nonce, contentHash),
	)
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}

func newNonce() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
