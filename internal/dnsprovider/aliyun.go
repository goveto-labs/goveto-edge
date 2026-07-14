package dnsprovider

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"goveto-edge/internal/storage/gen/model"
)

type aliyun struct {
	zone        string
	credentials Credentials
	client      *http.Client
}

func (*aliyun) SupportsLines() bool { return true }

func (a *aliyun) ListRecords(ctx context.Context, hostname string) ([]Record, error) {
	rr, err := RelativeName(hostname, a.zone)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0)
	for page := 1; ; page++ {
		var response struct {
			TotalCount    int `json:"TotalCount"`
			DomainRecords struct {
				Record []struct {
					ID    string `json:"RecordId"`
					RR    string `json:"RR"`
					Type  string `json:"Type"`
					Value string `json:"Value"`
					Line  string `json:"Line"`
					TTL   int    `json:"TTL"`
				} `json:"Record"`
			} `json:"DomainRecords"`
		}
		if err := a.call(ctx, "DescribeDomainRecords", url.Values{
			"DomainName": {a.zone},
			"PageNumber": {fmt.Sprint(page)},
			"PageSize":   {"500"},
			"RRKeyWord":  {rr},
		}, &response); err != nil {
			return nil, err
		}
		for _, item := range response.DomainRecords.Record {
			if item.RR != rr || (item.Type != string(model.DNSRecordTypeA) && item.Type != string(model.DNSRecordTypeAAAA)) {
				continue
			}
			result = append(result, Record{
				ID: item.ID, Hostname: hostname, Type: model.DNSRecordType(item.Type),
				Value: item.Value, Line: lineOrDefault(item.Line), TTL: item.TTL,
			})
		}
		if len(response.DomainRecords.Record) == 0 || page*500 >= response.TotalCount {
			break
		}
	}
	return result, nil
}

func (a *aliyun) ListDomains(ctx context.Context) ([]Domain, error) {
	result := make([]Domain, 0)
	for page := 1; ; page++ {
		var response struct {
			TotalCount int `json:"TotalCount"`
			Domains    struct {
				Domain []struct {
					Name string `json:"DomainName"`
					ID   string `json:"DomainId"`
				} `json:"Domain"`
			} `json:"Domains"`
		}
		if err := a.call(ctx, "DescribeDomains", url.Values{
			"PageNumber": {fmt.Sprint(page)},
			"PageSize":   {"100"},
		}, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Domains.Domain {
			result = append(result, Domain{Name: strings.ToLower(item.Name), ID: item.ID})
		}
		if len(response.Domains.Domain) == 0 || len(result) >= response.TotalCount {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (a *aliyun) ListLines(ctx context.Context) ([]Line, error) {
	var response struct {
		RecordLines struct {
			RecordLine []struct {
				Code        string `json:"LineCode"`
				ParentCode  string `json:"FatherCode"`
				Name        string `json:"LineName"`
				DisplayName string `json:"LineDisplayName"`
			} `json:"RecordLine"`
		} `json:"RecordLines"`
	}
	if err := a.call(ctx, "DescribeSupportLines", url.Values{
		"DomainName": {a.zone},
		"Lang":       {"zh"},
	}, &response); err != nil {
		return nil, err
	}
	result := make([]Line, 0, len(response.RecordLines.RecordLine))
	seen := map[string]bool{}
	for index, item := range response.RecordLines.RecordLine {
		code := strings.ToLower(strings.TrimSpace(item.Code))
		if code == "" || seen[code] {
			continue
		}
		name := strings.TrimSpace(item.DisplayName)
		if name == "" {
			name = strings.TrimSpace(item.Name)
		}
		if name == "" {
			name = code
		}
		if code == "default" {
			name = "默认"
		}
		seen[code] = true
		result = append(result, Line{
			Name:       name,
			Code:       code,
			ParentCode: strings.ToLower(strings.TrimSpace(item.ParentCode)),
			SortOrder:  index,
		})
	}
	if !seen["default"] {
		result = append([]Line{{Name: "默认", Code: "default", SortOrder: -1}}, result...)
	}
	return result, nil
}

func (a *aliyun) Upsert(ctx context.Context, record Record) (string, error) {
	rr, err := RelativeName(record.Hostname, a.zone)
	if err != nil {
		return "", err
	}
	id := record.ID
	if id == "" {
		id, err = a.find(ctx, rr, record)
		if err != nil {
			return "", err
		}
	}
	params := url.Values{"RR": {rr}, "Type": {string(record.Type)}, "Value": {record.Value}, "TTL": {fmt.Sprint(record.TTL)}, "Line": {lineOrDefault(record.Line)}}
	action := "AddDomainRecord"
	if id != "" {
		action = "UpdateDomainRecord"
		params.Set("RecordId", id)
	} else {
		params.Set("DomainName", a.zone)
	}
	var response struct {
		RecordID string `json:"RecordId"`
	}
	if err := a.call(ctx, action, params, &response); err != nil {
		return "", err
	}
	return response.RecordID, nil
}

func (a *aliyun) Delete(ctx context.Context, record Record) error {
	id := record.ID
	if id == "" {
		rr, err := RelativeName(record.Hostname, a.zone)
		if err != nil {
			return err
		}
		id, err = a.find(ctx, rr, record)
		if err != nil {
			return err
		}
	}
	if id == "" {
		return nil
	}
	return a.call(ctx, "DeleteDomainRecord", url.Values{"RecordId": {id}}, nil)
}

func (a *aliyun) find(ctx context.Context, rr string, record Record) (string, error) {
	params := url.Values{
		"DomainName":  {a.zone},
		"PageSize":    {"500"},
		"RRKeyWord":   {rr},
		"TypeKeyWord": {string(record.Type)},
	}
	var response struct {
		DomainRecords struct {
			Record []struct{ RecordID, RR, Value, Line string } `json:"Record"`
		} `json:"DomainRecords"`
	}
	if err := a.call(ctx, "DescribeDomainRecords", params, &response); err != nil {
		return "", err
	}
	for _, item := range response.DomainRecords.Record {
		if item.RR == rr && item.Value == record.Value && item.Line == lineOrDefault(record.Line) {
			return item.RecordID, nil
		}
	}
	return "", nil
}

func (a *aliyun) call(ctx context.Context, action string, params url.Values, output any) error {
	params.Set("AccessKeyId", a.credentials.AccessKeyID)
	params.Set("Action", action)
	params.Set("Format", "JSON")
	params.Set("SignatureMethod", "HMAC-SHA1")
	params.Set("SignatureNonce", uuid.NewString())
	params.Set("SignatureVersion", "1.0")
	params.Set("Timestamp", time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	params.Set("Version", "2015-01-09")
	canonical := canonicalQuery(params)
	stringToSign := "GET&%2F&" + escape(canonical)
	mac := hmac.New(sha1.New, []byte(a.credentials.AccessKeySecret+"&"))
	_, _ = mac.Write([]byte(stringToSign))
	params.Set("Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://alidns.aliyuncs.com/?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("Aliyun DNS API %s: %s", res.Status, string(data))
	}
	var apiError struct{ Code, Message string }
	_ = json.Unmarshal(data, &apiError)
	if apiError.Code != "" {
		return fmt.Errorf("Aliyun DNS API %s: %s", apiError.Code, apiError.Message)
	}
	if output != nil {
		return json.Unmarshal(data, output)
	}
	return nil
}

func canonicalQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, escape(key)+"="+escape(values.Get(key)))
	}
	return strings.Join(parts, "&")
}
func escape(value string) string {
	escaped := strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
	escaped = strings.ReplaceAll(escaped, "*", "%2A")
	return strings.ReplaceAll(escaped, "%7E", "~")
}
func lineOrDefault(line string) string {
	if strings.TrimSpace(line) == "" {
		return "default"
	}
	return line
}
