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
	"sync"
	"time"

	"github.com/google/uuid"

	"goveto-edge/internal/storage/gen/model"
)

type aliyun struct {
	zone        string
	credentials Credentials
	client      *http.Client

	linesMu     sync.Mutex
	linesLoaded bool
	lineLabels  map[string]string
}

func init() {
	Register(model.DNSProviderTypeALIYUN, &Descriptor{
		Name: "Aliyun",
		CredentialFields: []CredentialField{
			{Name: "access_key_id", Label: "AccessKey ID", Required: true},
			{Name: "access_key_secret", Label: "AccessKey Secret", Required: true, Secret: true},
		},
		Capabilities: Capabilities{Lines: true},
		Factory: func(config Config) (Provider, error) {
			return &aliyun{zone: config.Zone, credentials: config.Credentials, client: config.Client}, nil
		},
		ValidateCredentials: func(credentials Credentials) error {
			if credentials.AccessKeyID == "" || credentials.AccessKeySecret == "" {
				return fmt.Errorf("Aliyun access_key_id and access_key_secret are required")
			}
			return nil
		},
		ParseCredentials: func(input map[string]string) (Credentials, error) {
			accessKeyID := strings.TrimSpace(input["access_key_id"])
			accessKeySecret := strings.TrimSpace(input["access_key_secret"])
			if accessKeyID == "" || accessKeySecret == "" {
				return Credentials{}, fmt.Errorf("Aliyun access_key_id and access_key_secret are required")
			}
			return Credentials{AccessKeyID: accessKeyID, AccessKeySecret: accessKeySecret}, nil
		},
	})
}

func (*aliyun) Capabilities() Capabilities { return Capabilities{Lines: true} }

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
			if !strings.EqualFold(item.RR, rr) || (item.Type != string(model.DNSRecordTypeA) && item.Type != string(model.DNSRecordTypeAAAA)) {
				continue
			}
			result = append(result, Record{
				ID: item.ID, Hostname: hostname, Type: model.DNSRecordType(item.Type),
				// Record listings report localized line labels while writes take
				// line codes, so translate before callers compare keys.
				Value: item.Value, Line: a.lineCodeFor(ctx, item.Line), TTL: item.TTL,
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
	record.Value = CanonicalValue(record.Type, record.Value)
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
		if !IsRecordAlreadyExists(err) {
			return "", err
		}
		// UpdateDomainRecord can also report a duplicate when the locally
		// tracked id is stale and another provider record already represents
		// the desired value. In either case, locate and adopt that record.
		id, findErr := a.find(ctx, rr, record)
		if findErr != nil {
			return "", findErr
		}
		if id != "" {
			return id, nil
		}
		// The duplicate may collide on a line label the exact lookup cannot
		// translate back to a code. Adopt the record when RR+type+value match
		// it uniquely, so an untranslatable label cannot wedge the sync.
		if relaxedID, relaxedErr := a.findIgnoringLine(ctx, rr, record); relaxedErr == nil && relaxedID != "" {
			return relaxedID, nil
		}
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
	err := a.call(ctx, "DeleteDomainRecord", url.Values{"RecordId": {id}}, nil)
	if IsRecordNotFound(err) {
		return nil
	}
	return err
}

func (a *aliyun) find(ctx context.Context, rr string, record Record) (string, error) {
	wantedValue := CanonicalValue(record.Type, record.Value)
	for page := 1; ; page++ {
		params := url.Values{
			"DomainName":  {a.zone},
			"PageNumber":  {fmt.Sprint(page)},
			"PageSize":    {"500"},
			"RRKeyWord":   {rr},
			"TypeKeyWord": {string(record.Type)},
		}
		var response struct {
			TotalCount    int `json:"TotalCount"`
			DomainRecords struct {
				Record []struct {
					RecordID string `json:"RecordId"`
					RR       string `json:"RR"`
					Type     string `json:"Type"`
					Value    string `json:"Value"`
					Line     string `json:"Line"`
				} `json:"Record"`
			} `json:"DomainRecords"`
		}
		if err := a.call(ctx, "DescribeDomainRecords", params, &response); err != nil {
			return "", err
		}
		for _, item := range response.DomainRecords.Record {
			if strings.EqualFold(item.RR, rr) && CanonicalValue(record.Type, item.Value) == wantedValue &&
				a.lineCodeFor(ctx, item.Line) == normalizeLine(record.Line) {
				return item.RecordID, nil
			}
		}
		if len(response.DomainRecords.Record) == 0 || page*500 >= response.TotalCount {
			break
		}
	}
	return "", nil
}

// findIgnoringLine looks up a record by RR, type, and value only. It backs
// the duplicate-recovery path: Aliyun may report lines under labels that do
// not translate back to the code used for writes, and a single match still
// identifies the record the add collided with. Ambiguous matches (the same
// value published on several lines) are rejected.
func (a *aliyun) findIgnoringLine(ctx context.Context, rr string, record Record) (string, error) {
	wantedValue := CanonicalValue(record.Type, record.Value)
	matches := 0
	matchID := ""
	for page := 1; ; page++ {
		params := url.Values{
			"DomainName":  {a.zone},
			"PageNumber":  {fmt.Sprint(page)},
			"PageSize":    {"500"},
			"RRKeyWord":   {rr},
			"TypeKeyWord": {string(record.Type)},
		}
		var response struct {
			TotalCount    int `json:"TotalCount"`
			DomainRecords struct {
				Record []struct {
					RecordID string `json:"RecordId"`
					RR       string `json:"RR"`
					Type     string `json:"Type"`
					Value    string `json:"Value"`
				} `json:"Record"`
			} `json:"DomainRecords"`
		}
		if err := a.call(ctx, "DescribeDomainRecords", params, &response); err != nil {
			return "", err
		}
		for _, item := range response.DomainRecords.Record {
			if strings.EqualFold(item.RR, rr) && item.Type == string(record.Type) &&
				CanonicalValue(record.Type, item.Value) == wantedValue {
				matches++
				matchID = item.RecordID
			}
		}
		if len(response.DomainRecords.Record) == 0 || page*500 >= response.TotalCount {
			break
		}
	}
	if matches == 1 {
		return matchID, nil
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
	var apiError struct{ Code, Message string }
	_ = json.Unmarshal(data, &apiError)
	if res.StatusCode >= 300 || apiError.Code != "" {
		if apiError.Message == "" {
			apiError.Message = string(data)
		}
		return classifyAliyunError(res, apiError.Code, apiError.Message)
	}
	if output != nil {
		return json.Unmarshal(data, output)
	}
	return nil
}

// aliyunThrottleRetry is used when Aliyun throttles without a retry hint.
// Aliyun DNS API throttling typically clears within seconds.
const aliyunThrottleRetry = 5 * time.Second

// classifyAliyunError translates known Aliyun DNS error codes into package
// sentinel errors so callers never match vendor strings.
func classifyAliyunError(res *http.Response, code, message string) error {
	apiErr := &APIError{Provider: "Aliyun", Status: res.Status, Code: code, Message: message}
	switch {
	case code == "DomainRecordDuplicate":
		return fmt.Errorf("%w: %w", ErrRecordExists, apiErr)
	case code == "InvalidRecordId.NotFound":
		return fmt.Errorf("%w: %w", ErrRecordNotFound, apiErr)
	case strings.HasPrefix(code, "Throttling"), res.StatusCode == http.StatusTooManyRequests:
		// The Throttling.* codes carry a JSON body; a bare 429 comes from the
		// gateway in front of the API and has no error code.
		return &RateLimitedError{Provider: "Aliyun", RetryAfter: aliyunThrottleRetry, Err: apiErr}
	case code == "InvalidAccessKeyId.NotFound" || code == "InvalidAccessKeyId.Disabled" ||
		code == "SignatureDoesNotMatch" || code == "IncompleteSignature":
		return fmt.Errorf("%w: %w", ErrInvalidCredentials, apiErr)
	}
	return apiErr
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

// staticLineLabels translates the line labels DescribeDomainRecords most
// commonly reports into the line codes record writes accept. Aliyun accepts
// English codes on AddDomainRecord/UpdateDomainRecord but answers record
// listings with localized labels (e.g. "默认", "中国电信"), so reads must be
// translated back before they are compared with the codes we send.
var staticLineLabels = map[string]string{
	"default":       "default",
	"默认":            "default",
	"telecom":       "telecom",
	"中国电信":          "telecom",
	"china telecom": "telecom",
	"unicom":        "unicom",
	"中国联通":          "unicom",
	"china unicom":  "unicom",
	"mobile":        "mobile",
	"中国移动":          "mobile",
	"china mobile":  "mobile",
	"oversea":       "oversea",
	"境外":            "oversea",
	"海外":            "oversea",
}

// lineCodeFor maps a line label from a record listing to the canonical line
// code. Labels the zone's supported-lines endpoint knows are translated
// through it; unknown labels fall back to lowercasing so comparisons stay
// deterministic.
func (a *aliyun) lineCodeFor(ctx context.Context, label string) string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return "default"
	}
	if code, ok := a.lineLabelCodes(ctx)[strings.ToLower(trimmed)]; ok {
		return code
	}
	return strings.ToLower(trimmed)
}

// lineLabelCodes returns the label-to-code mapping, seeded with the static
// labels and enriched once per provider instance from the zone's supported
// lines. A failing lines lookup degrades to the static labels rather than
// failing the record operation.
func (a *aliyun) lineLabelCodes(ctx context.Context) map[string]string {
	a.linesMu.Lock()
	defer a.linesMu.Unlock()
	if a.linesLoaded {
		return a.lineLabels
	}
	codes := make(map[string]string, len(staticLineLabels)+16)
	for label, code := range staticLineLabels {
		codes[label] = code
	}
	if lines, err := a.ListLines(ctx); err == nil {
		for _, line := range lines {
			code := strings.ToLower(strings.TrimSpace(line.Code))
			if code == "" {
				continue
			}
			codes[code] = code
			if name := strings.ToLower(strings.TrimSpace(line.Name)); name != "" {
				codes[name] = code
			}
		}
	}
	a.lineLabels = codes
	a.linesLoaded = true
	return codes
}

func normalizeLine(line string) string {
	return strings.ToLower(lineOrDefault(strings.TrimSpace(line)))
}
