package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

type cloudflare struct {
	zone, zoneID, token string
	client              *http.Client
}

func init() {
	Register(model.DNSProviderTypeCLOUDFLARE, &Descriptor{
		Name: "Cloudflare",
		CredentialFields: []CredentialField{
			{Name: "api_token", Label: "API Token", Required: true, Secret: true},
		},
		RequiresZoneID: true,
		Capabilities:   Capabilities{Proxied: true},
		Factory: func(config Config) (Provider, error) {
			return &cloudflare{
				zone: config.Zone, zoneID: config.ZoneID,
				token: config.Credentials.APIToken, client: config.Client,
			}, nil
		},
		Validate: func(config Config) error {
			if config.ZoneID == "" {
				return fmt.Errorf("Cloudflare zone_id is required")
			}
			return nil
		},
		ValidateCredentials: func(credentials Credentials) error {
			if credentials.APIToken == "" {
				return fmt.Errorf("Cloudflare api_token is required")
			}
			return nil
		},
		ParseCredentials: func(input map[string]string) (Credentials, error) {
			apiToken := strings.TrimSpace(input["api_token"])
			if apiToken == "" {
				return Credentials{}, fmt.Errorf("Cloudflare api_token is required")
			}
			return Credentials{APIToken: apiToken}, nil
		},
	})
}

// ListLines reports the single default line. Cloudflare has no regional line
// concept, so zone credentials are irrelevant here.
func (*cloudflare) ListLines(context.Context) ([]Line, error) {
	return []Line{{Name: "Default", Code: "default", SortOrder: 0}}, nil
}

func (*cloudflare) Capabilities() Capabilities { return Capabilities{Proxied: true} }

func (c *cloudflare) ListRecords(ctx context.Context, hostname string) ([]Record, error) {
	if _, err := RelativeName(hostname, c.zone); err != nil {
		return nil, err
	}
	result := make([]Record, 0)
	for page := 1; ; page++ {
		var response struct {
			Result []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Type    string `json:"type"`
				Content string `json:"content"`
				TTL     int    `json:"ttl"`
				Proxied bool   `json:"proxied"`
			} `json:"result"`
			ResultInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		path := fmt.Sprintf(
			"/zones/%s/dns_records?per_page=100&page=%d&name=%s",
			c.zoneID,
			page,
			url.QueryEscape(hostname),
		)
		if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Result {
			if item.Name != hostname || (item.Type != string(model.DNSRecordTypeA) && item.Type != string(model.DNSRecordTypeAAAA)) {
				continue
			}
			result = append(result, Record{
				ID: item.ID, Hostname: hostname, Type: model.DNSRecordType(item.Type),
				Value: item.Content, Line: "default", TTL: item.TTL, Proxied: item.Proxied,
			})
		}
		if len(response.Result) == 0 || response.ResultInfo.TotalPages == 0 || page >= response.ResultInfo.TotalPages {
			break
		}
	}
	return result, nil
}

func (c *cloudflare) ListDomains(ctx context.Context) ([]Domain, error) {
	result := make([]Domain, 0)
	for page := 1; ; page++ {
		var response struct {
			Result []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"result"`
			ResultInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		path := fmt.Sprintf("/zones?per_page=50&page=%d", page)
		if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Result {
			result = append(result, Domain{Name: item.Name, ID: item.ID})
		}
		if len(response.Result) == 0 || response.ResultInfo.TotalPages == 0 || page >= response.ResultInfo.TotalPages {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (c *cloudflare) Upsert(ctx context.Context, record Record) (string, error) {
	if _, err := RelativeName(record.Hostname, c.zone); err != nil {
		return "", err
	}
	record.TTL = ExpectedTTL(record)
	record.Value = CanonicalValue(record.Type, record.Value)
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
		if method != http.MethodPost || !IsRecordAlreadyExists(err) {
			return "", err
		}
		id, findErr := c.find(ctx, record)
		if findErr != nil {
			return "", findErr
		}
		if id != "" {
			return id, nil
		}
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
	err := c.do(ctx, http.MethodDelete, "/zones/"+c.zoneID+"/dns_records/"+id, nil, nil)
	if IsRecordNotFound(err) {
		return nil
	}
	return err
}

func (c *cloudflare) find(ctx context.Context, record Record) (string, error) {
	wantedValue := CanonicalValue(record.Type, record.Value)
	for page := 1; ; page++ {
		path := "/zones/" + c.zoneID + "/dns_records?per_page=100&page=" + strconv.Itoa(page) +
			"&type=" + url.QueryEscape(string(record.Type)) + "&name=" + url.QueryEscape(record.Hostname)
		var response struct {
			Result     []struct{ ID, Content string } `json:"result"`
			ResultInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return "", err
		}
		for _, item := range response.Result {
			if CanonicalValue(record.Type, item.Content) == wantedValue {
				return item.ID, nil
			}
		}
		if len(response.Result) == 0 || response.ResultInfo.TotalPages == 0 || page >= response.ResultInfo.TotalPages {
			break
		}
	}
	return "", nil
}

// classifyCloudflareError translates known Cloudflare API failures into
// package sentinel errors so callers never match vendor strings.
func classifyCloudflareError(res *http.Response, apiErr *APIError) error {
	switch {
	case apiErr.Code == "81057" || apiErr.Code == "81058":
		return fmt.Errorf("%w: %w", ErrRecordExists, apiErr)
	case apiErr.Code == "81044":
		return fmt.Errorf("%w: %w", ErrRecordNotFound, apiErr)
	case res.StatusCode == http.StatusTooManyRequests:
		return &RateLimitedError{
			Provider:   "Cloudflare",
			RetryAfter: parseRetryAfter(res.Header.Get("Retry-After"), time.Now()),
			Err:        apiErr,
		}
	case res.StatusCode == http.StatusUnauthorized ||
		apiErr.Code == "9103" || apiErr.Code == "9104" || apiErr.Code == "9105" ||
		// 9109 is "Invalid access token" (returned with HTTP 403) and 6003 is
		// "Invalid request headers" (malformed authentication headers, with an
		// error chain such as 6103 or 6111); both are credential failures.
		apiErr.Code == "9109" || apiErr.Code == "6003":
		return fmt.Errorf("%w: %w", ErrInvalidCredentials, apiErr)
	}
	return apiErr
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
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(data, &envelope)
	if res.StatusCode >= 300 || !envelope.Success {
		apiError := &APIError{Provider: "Cloudflare", Status: res.Status}
		if len(envelope.Errors) > 0 {
			apiError.Code = strconv.Itoa(envelope.Errors[0].Code)
			apiError.Message = envelope.Errors[0].Message
		}
		if apiError.Message == "" {
			apiError.Message = string(data)
		}
		return classifyCloudflareError(res, apiError)
	}
	if output != nil {
		return json.Unmarshal(data, output)
	}
	return nil
}
