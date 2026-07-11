// Package edgecontrol implements control-plane communication with edge agents.
package edgecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"goveto-edge/internal/edgeagent"
)

type Client struct {
	baseURL, nodeID, key string
	http                 *http.Client
}

func New(baseURL, nodeID, communicationKey string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), nodeID: nodeID, key: communicationKey, http: &http.Client{Timeout: 75 * time.Second}}
}

func (c *Client) PushSiteConfig(ctx context.Context, config edgeagent.SiteConfig) error {
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	request, err := c.request(ctx, http.MethodPut, "/v1/sites/"+config.SiteID+"/config", bytes.NewReader(body))
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("agent rejected site config: %s", response.Status)
	}
	return nil
}

// PullLogs continuously long-polls the agent. Records are acknowledged only
// after consume returns successfully, so transient control-plane failures do
// not lose data.
func (c *Client) PullLogs(ctx context.Context, consume func(context.Context, []edgeagent.LogRecord) error) error {
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
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}
		var payload struct {
			Records []edgeagent.LogRecord `json:"records"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil {
			return fmt.Errorf("pull agent logs: %s", response.Status)
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

func (c *Client) ack(ctx context.Context, through uint64) error {
	body, _ := json.Marshal(map[string]uint64{"through": through})
	request, err := c.request(ctx, http.MethodPost, "/v1/logs/ack", bytes.NewReader(body))
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

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.key)
	request.Header.Set("X-Goveto-Node-ID", c.nodeID)
	request.Host = c.nodeID
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}
