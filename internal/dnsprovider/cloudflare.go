package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type cloudflare struct {
	zone, zoneID, token string
	client              *http.Client
}

func (*cloudflare) SupportsLines() bool { return false }

func (c *cloudflare) Upsert(ctx context.Context, record Record) (string, error) {
	if _, err := RelativeName(record.Hostname, c.zone); err != nil {
		return "", err
	}
	if record.Proxied {
		record.TTL = 1
	}
	id := record.ID
	if id == "" {
		found, err := c.find(ctx, record)
		if err != nil {
			return "", err
		}
		id = found
	}
	payload := map[string]any{"type": record.Type, "name": record.Hostname, "content": record.Value, "ttl": record.TTL, "proxied": record.Proxied}
	method, path := http.MethodPost, "/zones/"+c.zoneID+"/dns_records"
	if id != "" {
		method, path = http.MethodPut, path+"/"+id
	}
	var response struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := c.do(ctx, method, path, payload, &response); err != nil {
		return "", err
	}
	return response.Result.ID, nil
}

func (c *cloudflare) Delete(ctx context.Context, record Record) error {
	if _, err := RelativeName(record.Hostname, c.zone); err != nil {
		return err
	}
	id := record.ID
	if id == "" {
		var err error
		id, err = c.find(ctx, record)
		if err != nil {
			return err
		}
	}
	if id == "" {
		return nil
	}
	return c.do(ctx, http.MethodDelete, "/zones/"+c.zoneID+"/dns_records/"+id, nil, nil)
}

func (c *cloudflare) find(ctx context.Context, record Record) (string, error) {
	path := "/zones/" + c.zoneID + "/dns_records?per_page=100&type=" +
		url.QueryEscape(string(record.Type)) + "&name=" + url.QueryEscape(record.Hostname)
	var response struct {
		Result []struct{ ID, Content string } `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return "", err
	}
	for _, item := range response.Result {
		if item.Content == record.Value {
			return item.ID, nil
		}
	}
	return "", nil
}

func (c *cloudflare) do(ctx context.Context, method, path string, payload any, output any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.cloudflare.com/client/v4"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(data, &envelope)
	if res.StatusCode >= 300 || !envelope.Success {
		return fmt.Errorf("Cloudflare DNS API %s: %s", res.Status, string(data))
	}
	if output != nil {
		return json.Unmarshal(data, output)
	}
	return nil
}
