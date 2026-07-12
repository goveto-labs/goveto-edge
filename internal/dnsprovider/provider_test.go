package dnsprovider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRelativeName(t *testing.T) {
	name, err := RelativeName("edge.hk.example.com.", "example.com")
	if err != nil || name != "edge.hk" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if _, err = RelativeName("example.net", "example.com"); err == nil {
		t.Fatal("expected outside-zone error")
	}
}

func TestCloudflareUpsertCreatesRecord(t *testing.T) {
	var got *http.Request
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		got = request
		body := `{"success":true,"result":[]}`
		if request.Method == http.MethodPost {
			body = `{"success":true,"result":{"id":"record-1"}}`
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	provider, err := New(model.DNSProviderTypeCLOUDFLARE, "example.com", "zone-1", []byte(`{"api_token":"secret"}`), client)
	if err != nil {
		t.Fatal(err)
	}
	id, err := provider.Upsert(context.Background(), Record{Hostname: "www.example.com", Type: model.DNSRecordTypeCNAME, Value: "edge.example.com", TTL: 300})
	if err != nil || id != "record-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if got == nil || got.Method != http.MethodPost || got.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("unexpected request: %#v", got)
	}
	if provider.SupportsLines() {
		t.Fatal("Cloudflare must not advertise regional line support")
	}
}

func TestCloudflareRejectsRecordOutsideZone(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})}
	provider, err := New(
		model.DNSProviderTypeCLOUDFLARE,
		"example.com",
		"zone-1",
		[]byte(`{"api_token":"secret"}`),
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Upsert(context.Background(), Record{
		Hostname: "www.example.net",
		Type:     model.DNSRecordTypeCNAME,
		Value:    "edge.example.com",
		TTL:      300,
	}); err == nil {
		t.Fatal("expected outside-zone error")
	}
	if called {
		t.Fatal("outside-zone record must not reach Cloudflare")
	}
}

func TestAliyunRequestIsSigned(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		query := request.URL.Query()
		if query.Get("Signature") == "" || query.Get("AccessKeyId") != "key" {
			t.Fatalf("unsigned request: %s", request.URL.String())
		}
		body := `{"DomainRecords":{"Record":[]}}`
		if query.Get("Action") == "DescribeDomainRecords" && query.Get("PageSize") != "500" {
			t.Fatalf("page size=%q", query.Get("PageSize"))
		}
		if query.Get("Action") == "AddDomainRecord" {
			if query.Get("Line") != "telecom" {
				t.Fatalf("line=%q", query.Get("Line"))
			}
			body = `{"RecordId":"record-2"}`
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	provider, err := New(model.DNSProviderTypeALIYUN, "example.com", "", []byte(`{"access_key_id":"key","access_key_secret":"secret"}`), client)
	if err != nil {
		t.Fatal(err)
	}
	id, err := provider.Upsert(context.Background(), Record{Hostname: "edge.example.com", Type: model.DNSRecordTypeA, Value: "192.0.2.1", Line: "telecom", TTL: 300})
	if err != nil || id != "record-2" || requests != 2 {
		t.Fatalf("id=%q requests=%d err=%v", id, requests, err)
	}
	if !provider.SupportsLines() {
		t.Fatal("Aliyun should support regional lines")
	}
}
