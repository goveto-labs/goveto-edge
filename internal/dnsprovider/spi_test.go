package dnsprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

func TestRegistryDescriptorsExposeCapabilities(t *testing.T) {
	aliyun, err := DescriptorFor(model.DNSProviderTypeALIYUN)
	if err != nil {
		t.Fatal(err)
	}
	if aliyun.Name != "Aliyun" || !aliyun.Capabilities.Lines || aliyun.RequiresZoneID {
		t.Fatalf("unexpected Aliyun descriptor: %+v", aliyun)
	}
	if len(aliyun.CredentialFields) != 2 || !aliyun.CredentialFields[1].Secret {
		t.Fatalf("Aliyun credential fields must describe a secret AccessKey: %+v", aliyun.CredentialFields)
	}
	cloudflare, err := DescriptorFor(model.DNSProviderTypeCLOUDFLARE)
	if err != nil {
		t.Fatal(err)
	}
	if cloudflare.Name != "Cloudflare" || cloudflare.Capabilities.Lines || !cloudflare.Capabilities.Proxied || !cloudflare.RequiresZoneID {
		t.Fatalf("unexpected Cloudflare descriptor: %+v", cloudflare)
	}
	if _, err = DescriptorFor(model.DNSProviderType("DNSPOD")); err == nil {
		t.Fatal("unregistered provider must be rejected")
	}
}

func TestNewValidatesZoneRequirements(t *testing.T) {
	if _, err := New(model.DNSProviderTypeALIYUN, "", "", []byte(`{"access_key_id":"k","access_key_secret":"s"}`), nil); err == nil {
		t.Fatal("missing zone must be rejected")
	}
	if _, err := New(model.DNSProviderTypeCLOUDFLARE, "example.com", "", []byte(`{"api_token":"t"}`), nil); err == nil {
		t.Fatal("Cloudflare without zone_id must be rejected for record operations")
	}
	// Discovery construction (empty zone) stays legal through the factory.
	credentials, descriptor, err := decodeCredentials(model.DNSProviderTypeCLOUDFLARE, []byte(`{"api_token":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = descriptor.Factory(Config{Credentials: credentials, Client: nil}); err != nil {
		t.Fatalf("discovery construction must not require a zone: %v", err)
	}
}

func TestSentinelErrorClassification(t *testing.T) {
	// Drive the real classifiers so the vendor code to sentinel mapping is
	// covered instead of hand-wrapped errors.
	res := &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Header: http.Header{}}

	exists := classifyAliyunError(res, "DomainRecordDuplicate", "record exists")
	if !errors.Is(exists, ErrRecordExists) || !IsRecordAlreadyExists(exists) {
		t.Fatalf("aliyun duplicate must map to ErrRecordExists, got %v", exists)
	}
	var apiErr *APIError
	if !errors.As(exists, &apiErr) || apiErr.Code != "DomainRecordDuplicate" {
		t.Fatalf("classified error must keep the underlying *APIError, got %v", exists)
	}
	if !IsRecordNotFound(classifyAliyunError(res, "InvalidRecordId.NotFound", "missing record")) {
		t.Fatal("aliyun missing record must map to ErrRecordNotFound")
	}

	duplicate := classifyCloudflareError(res, &APIError{Provider: "Cloudflare", Code: "81057", Message: "record exists"})
	if !IsRecordAlreadyExists(duplicate) {
		t.Fatalf("cloudflare duplicate must map to ErrRecordExists, got %v", duplicate)
	}
	notFound := classifyCloudflareError(res, &APIError{Provider: "Cloudflare", Code: "81044", Message: "record does not exist"})
	if !IsRecordNotFound(notFound) {
		t.Fatalf("cloudflare missing record must map to ErrRecordNotFound, got %v", notFound)
	}

	plain := classifyAliyunError(res, "Whatever", "unclassified")
	if IsRecordAlreadyExists(plain) || IsRecordNotFound(plain) {
		t.Fatal("unclassified errors must not match sentinels")
	}
	if IsRecordAlreadyExists(nil) || IsRecordNotFound(nil) {
		t.Fatal("nil must not match sentinels")
	}
}

func TestCloudflareInvalidTokenMapsToInvalidCredentials(t *testing.T) {
	res := &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: http.Header{}}
	for _, code := range []string{"9109", "6003"} {
		err := classifyCloudflareError(res, &APIError{Provider: "Cloudflare", Code: code})
		if !IsInvalidCredentials(err) {
			t.Fatalf("code %s must map to ErrInvalidCredentials, got %v", code, err)
		}
	}
}

func TestAliyunThrottlingMapsToRateLimited(t *testing.T) {
	res := &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Header: http.Header{}}
	err := classifyAliyunError(res, "Throttling.User", "Requests throttled")
	if !IsRateLimited(err) {
		t.Fatalf("throttling must map to ErrRateLimited, got %v", err)
	}
	if got := RateLimitRetryAfter(err); got != aliyunThrottleRetry {
		t.Fatalf("retry hint = %s, want %s", got, aliyunThrottleRetry)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatal("rate limited error must keep the underlying *APIError")
	}
	// A bare 429 without a JSON error code is gateway throttling and must
	// still classify as rate limited.
	gateway := &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests", Header: http.Header{}}
	if limited := classifyAliyunError(gateway, "", "Too Many Requests"); !IsRateLimited(limited) {
		t.Fatalf("bare 429 must map to ErrRateLimited, got %v", limited)
	}
	invalid := classifyAliyunError(res, "InvalidAccessKeyId.NotFound", "bad key")
	if !IsInvalidCredentials(invalid) {
		t.Fatalf("rejected key must map to ErrInvalidCredentials, got %v", invalid)
	}
}

func TestCloudflareRateLimitParsesRetryAfterHeader(t *testing.T) {
	res := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	res.Header.Set("Retry-After", "17")
	err := classifyCloudflareError(res, &APIError{Provider: "Cloudflare", Code: "971", Message: "rate limited"})
	if !IsRateLimited(err) {
		t.Fatalf("429 must map to ErrRateLimited, got %v", err)
	}
	if got := RateLimitRetryAfter(err); got != 17*time.Second {
		t.Fatalf("retry hint = %s, want 17s", got)
	}

	res.Header.Set("Retry-After", "0")
	if got := RateLimitRetryAfter(classifyCloudflareError(res, &APIError{Provider: "Cloudflare"})); got != 0 {
		t.Fatalf("zero hint must mean default backoff, got %s", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("", now); got != 0 {
		t.Fatalf("empty header = %s, want 0", got)
	}
	if got := parseRetryAfter("42", now); got != 42*time.Second {
		t.Fatalf("seconds form = %s, want 42s", got)
	}
	if got := parseRetryAfter(now.Add(time.Minute).UTC().Format(http.TimeFormat), now); got < 55*time.Second || got > time.Minute {
		t.Fatalf("http-date form = %s, want ~60s", got)
	}
	if got := parseRetryAfter("not-a-date", now); got != 0 {
		t.Fatalf("invalid header = %s, want 0", got)
	}
	if got := parseRetryAfter("-5", now); got != 0 {
		t.Fatalf("negative header = %s, want 0", got)
	}
	if got := parseRetryAfter("3600", now); got != maxRetryAfter {
		t.Fatalf("oversized seconds = %s, want clamped to %s", got, maxRetryAfter)
	}
	if got := parseRetryAfter(now.Add(time.Hour).UTC().Format(http.TimeFormat), now); got != maxRetryAfter {
		t.Fatalf("oversized http-date = %s, want clamped to %s", got, maxRetryAfter)
	}
}

func TestProbeValidatesCredentialsAndListsZones(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		body := `{"success":true,"result":[{"id":"zone-1","name":"example.com"}],"result_info":{"total_pages":1}}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	result, err := Probe(context.Background(), model.DNSProviderTypeCLOUDFLARE, []byte(`{"api_token":"token"}`), client)
	if err != nil || calls != 1 || result.Provider != "Cloudflare" {
		t.Fatalf("probe result=%+v calls=%d err=%v", result, calls, err)
	}
	if len(result.Zones) != 1 || result.Zones[0].ID != "zone-1" {
		t.Fatalf("zones=%+v", result.Zones)
	}
	// Incomplete credentials fail before any HTTP call.
	if _, err = Probe(context.Background(), model.DNSProviderTypeCLOUDFLARE, []byte(`{}`), client); err == nil || calls != 1 {
		t.Fatalf("incomplete credentials must fail fast, err=%v calls=%d", err, calls)
	}
}

func TestParseCredentialsSanitizesInput(t *testing.T) {
	descriptor, err := DescriptorFor(model.DNSProviderTypeALIYUN)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := descriptor.ParseCredentials(map[string]string{
		"access_key_id":     "  key  ",
		"access_key_secret": " secret ",
	})
	if err != nil || parsed.AccessKeyID != "key" || parsed.AccessKeySecret != "secret" {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
	if _, err = descriptor.ParseCredentials(map[string]string{"access_key_id": "key"}); err == nil {
		t.Fatal("missing secret must be rejected")
	}
}

func TestExpectedTTL(t *testing.T) {
	if got := ExpectedTTL(Record{TTL: 300, Proxied: true}); got != 1 {
		t.Fatalf("proxied TTL = %d, want 1", got)
	}
	if got := ExpectedTTL(Record{TTL: 300}); got != 300 {
		t.Fatalf("plain TTL = %d, want 300", got)
	}
}

// newAliyunTestProvider builds an Aliyun provider whose transport answers
// from per-action handler functions, driving the real request/classification
// path instead of hand-wrapped errors.
func newAliyunTestProvider(t *testing.T, handlers map[string]func(query url.Values) string) (Provider, func() int) {
	t.Helper()
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		query := request.URL.Query()
		handler, ok := handlers[query.Get("Action")]
		if !ok {
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		body := handler(query)
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{},
		}, nil
	})}
	provider, err := New(model.DNSProviderTypeALIYUN, "example.com", "", []byte(`{"access_key_id":"key","access_key_secret":"secret"}`), client)
	if err != nil {
		t.Fatal(err)
	}
	return provider, func() int { return requests }
}

func aliyunSupportLinesBody() string {
	return `{"RecordLines":{"RecordLine":[` +
		`{"LineCode":"default","LineDisplayName":"默认"},` +
		`{"LineCode":"telecom","LineDisplayName":"中国电信"},` +
		`{"LineCode":"unicom","LineDisplayName":"中国联通"}]}}`
}

// Aliyun accepts English line codes on writes but reports localized labels in
// record listings. The exact lookup must translate labels back to codes or a
// duplicate record blocks every subsequent add.
func TestAliyunUpsertFindsRecordByLocalizedLineLabel(t *testing.T) {
	provider, requestCount := newAliyunTestProvider(t, map[string]func(query url.Values) string{
		"DescribeSupportLines": func(query url.Values) string { return aliyunSupportLinesBody() },
		"DescribeDomainRecords": func(query url.Values) string {
			return `{"TotalCount":1,"DomainRecords":{"Record":[{"RecordId":"rec-1","RR":"Edge","Type":"A","Value":"192.0.2.1","Line":"中国电信"}]}}`
		},
		"UpdateDomainRecord": func(query url.Values) string {
			if query.Get("RecordId") != "rec-1" || query.Get("Line") != "telecom" {
				t.Fatalf("update must target the adopted record with the line code: %s", query.Encode())
			}
			return `{"RecordId":"rec-1"}`
		},
	})
	id, err := provider.Upsert(context.Background(), Record{
		Hostname: "edge.example.com", Type: model.DNSRecordTypeA,
		Value: "192.0.2.1", Line: "telecom", TTL: 300,
	})
	if err != nil || id != "rec-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if got := requestCount(); got != 3 {
		t.Fatalf("requests = %d, want 3 (lines, lookup, update)", got)
	}
}

// Even when a label cannot be translated, a duplicate whose RR+type+value
// matches uniquely must be adopted instead of wedging the sync forever.
func TestAliyunUpsertAdoptsDuplicateWithUntranslatableLineLabel(t *testing.T) {
	listing := `{"TotalCount":1,"DomainRecords":{"Record":[{"RecordId":"rec-gd","RR":"edge","Type":"A","Value":"192.0.2.1","Line":"华南-广东电信"}]}}`
	added := false
	provider, _ := newAliyunTestProvider(t, map[string]func(query url.Values) string{
		"DescribeSupportLines": func(query url.Values) string { return aliyunSupportLinesBody() },
		"DescribeDomainRecords": func(query url.Values) string {
			return listing
		},
		"AddDomainRecord": func(query url.Values) string {
			added = true
			return `{"Code":"DomainRecordDuplicate","Message":"The DNS record already exists."}`
		},
	})
	id, err := provider.Upsert(context.Background(), Record{
		Hostname: "edge.example.com", Type: model.DNSRecordTypeA,
		Value: "192.0.2.1", Line: "cn-telecom-gd", TTL: 300,
	})
	if !added {
		t.Fatal("flow must exercise the duplicate path")
	}
	if err != nil || id != "rec-gd" {
		t.Fatalf("id=%q err=%v (want relaxed adoption of rec-gd)", id, err)
	}
}

// A stale locally tracked id makes Upsert choose UpdateDomainRecord. Aliyun
// reports DomainRecordDuplicate when another id already holds the desired
// record, so recovery must adopt that id just like the add path does.
func TestAliyunUpsertAdoptsDuplicateAfterStaleIDUpdate(t *testing.T) {
	updated := false
	provider, _ := newAliyunTestProvider(t, map[string]func(query url.Values) string{
		"DescribeSupportLines": func(query url.Values) string { return aliyunSupportLinesBody() },
		"DescribeDomainRecords": func(query url.Values) string {
			return `{"TotalCount":1,"DomainRecords":{"Record":[{"RecordId":"rec-current","RR":"edge","Type":"A","Value":"192.0.2.1","Line":"默认"}]}}`
		},
		"UpdateDomainRecord": func(query url.Values) string {
			updated = true
			if query.Get("RecordId") != "rec-stale" {
				t.Fatalf("update record id = %q, want stale tracked id", query.Get("RecordId"))
			}
			return `{"Code":"DomainRecordDuplicate","Message":"The DNS record already exists."}`
		},
	})
	id, err := provider.Upsert(context.Background(), Record{
		ID: "rec-stale", Hostname: "edge.example.com", Type: model.DNSRecordTypeA,
		Value: "192.0.2.1", Line: "default", TTL: 300,
	})
	if !updated {
		t.Fatal("flow must exercise the stale-id update path")
	}
	if err != nil || id != "rec-current" {
		t.Fatalf("id=%q err=%v (want adoption of rec-current)", id, err)
	}
}

// When the same value is published on several untranslatable lines, relaxed
// adoption is ambiguous and the duplicate error must surface instead of
// adopting an arbitrary record.
func TestAliyunUpsertDuplicateStaysAmbiguousAcrossLines(t *testing.T) {
	listing := `{"TotalCount":2,"DomainRecords":{"Record":[` +
		`{"RecordId":"rec-gd","RR":"edge","Type":"A","Value":"192.0.2.1","Line":"华南-广东电信"},` +
		`{"RecordId":"rec-js","RR":"edge","Type":"A","Value":"192.0.2.1","Line":"华东-江苏电信"}]}}`
	provider, _ := newAliyunTestProvider(t, map[string]func(query url.Values) string{
		"DescribeSupportLines": func(query url.Values) string { return aliyunSupportLinesBody() },
		"DescribeDomainRecords": func(query url.Values) string {
			return listing
		},
		"AddDomainRecord": func(query url.Values) string {
			return `{"Code":"DomainRecordDuplicate","Message":"The DNS record already exists."}`
		},
	})
	_, err := provider.Upsert(context.Background(), Record{
		Hostname: "edge.example.com", Type: model.DNSRecordTypeA,
		Value: "192.0.2.1", Line: "cn-telecom-gd", TTL: 300,
	})
	if !IsRecordAlreadyExists(err) {
		t.Fatalf("ambiguous duplicate must surface ErrRecordExists, got %v", err)
	}
}

// ListRecords must translate localized line labels (and tolerate RR case
// differences) so the sync layer compares records under stable keys.
func TestAliyunListRecordsTranslatesLineLabels(t *testing.T) {
	provider, _ := newAliyunTestProvider(t, map[string]func(query url.Values) string{
		"DescribeSupportLines": func(query url.Values) string { return aliyunSupportLinesBody() },
		"DescribeDomainRecords": func(query url.Values) string {
			return `{"TotalCount":2,"DomainRecords":{"Record":[` +
				`{"RecordId":"a-1","RR":"Edge","Type":"A","Value":"192.0.2.1","Line":"默认","TTL":300},` +
				`{"RecordId":"a-2","RR":"edge","Type":"A","Value":"192.0.2.2","Line":"中国联通","TTL":300}]}}`
		},
	})
	records, err := provider.ListRecords(context.Background(), "edge.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	byValue := map[string]Record{}
	for _, record := range records {
		byValue[record.Value] = record
	}
	if got := byValue["192.0.2.1"].Line; got != "default" {
		t.Fatalf("默认 must translate to default, got %q", got)
	}
	if got := byValue["192.0.2.2"].Line; got != "unicom" {
		t.Fatalf("中国联通 must translate to unicom, got %q", got)
	}
}
