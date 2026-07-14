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

func TestCloudflareListsNodeRecords(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"success":true,"result":[{"id":"a-1","name":"edge.example.com","type":"A","content":"192.0.2.1","ttl":300,"proxied":false},{"id":"txt-1","name":"edge.example.com","type":"TXT","content":"ignore"}],"result_info":{"total_pages":1}}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	provider, err := New(model.DNSProviderTypeCLOUDFLARE, "example.com", "zone-1", []byte(`{"api_token":"secret"}`), client)
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.ListRecords(context.Background(), "edge.example.com")
	if err != nil || len(records) != 1 || records[0].Value != "192.0.2.1" {
		t.Fatalf("records=%#v err=%v", records, err)
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

func TestAliyunListsNodeRecords(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if query.Get("Action") != "DescribeDomainRecords" || query.Get("RRKeyWord") != "edge" {
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		body := `{"TotalCount":2,"DomainRecords":{"Record":[{"RecordId":"a-1","RR":"edge","Type":"A","Value":"192.0.2.1","Line":"telecom","TTL":300},{"RecordId":"txt-1","RR":"edge","Type":"TXT","Value":"ignore"}]}}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	provider, err := New(model.DNSProviderTypeALIYUN, "example.com", "", []byte(`{"access_key_id":"key","access_key_secret":"secret"}`), client)
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.ListRecords(context.Background(), "edge.example.com")
	if err != nil || len(records) != 1 || records[0].Line != "telecom" {
		t.Fatalf("records=%#v err=%v", records, err)
	}
}

func TestAliyunDiscovery(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Query().Get("Action") {
		case "DescribeDomains":
			body = `{"TotalCount":1,"Domains":{"Domain":[{"DomainName":"Example.com","DomainId":"domain-1"}]}}`
		case "DescribeSupportLines":
			if request.URL.Query().Get("DomainName") != "example.com" {
				t.Fatalf("domain=%q", request.URL.Query().Get("DomainName"))
			}
			if request.URL.Query().Get("Lang") != "zh" {
				t.Fatalf("lang=%q", request.URL.Query().Get("Lang"))
			}
			body = `{"RecordLines":{"RecordLine":[{"LineCode":"default","LineDisplayName":"默认"},{"LineCode":"search","FatherCode":"default","LineDisplayName":"搜索引擎"},{"LineCode":"telecom","FatherCode":"cn","LineDisplayName":"中国电信"}]}}`
		default:
			t.Fatalf("unexpected action %q", request.URL.Query().Get("Action"))
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	raw := []byte(`{"access_key_id":"key","access_key_secret":"secret"}`)
	domains, err := ListDomains(context.Background(), model.DNSProviderTypeALIYUN, raw, client)
	if err != nil || len(domains) != 1 || domains[0].Name != "example.com" {
		t.Fatalf("domains=%#v err=%v", domains, err)
	}
	lines, err := ListLines(context.Background(), model.DNSProviderTypeALIYUN, domains[0].Name, domains[0].ID, raw, client)
	if err != nil || len(lines) != 3 ||
		lines[0].Code != "default" ||
		lines[1].Code != "search" ||
		lines[1].ParentCode != "default" ||
		lines[2].Code != "telecom" ||
		lines[2].SortOrder != 2 {
		t.Fatalf("lines=%#v err=%v", lines, err)
	}
}

func TestCloudflareDiscovery(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		body := `{"success":true,"result":[{"id":"zone-1","name":"example.com"}],"result_info":{"total_pages":1}}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	domains, err := ListDomains(context.Background(), model.DNSProviderTypeCLOUDFLARE, []byte(`{"api_token":"secret"}`), client)
	if err != nil || len(domains) != 1 || domains[0].ID != "zone-1" {
		t.Fatalf("domains=%#v err=%v", domains, err)
	}
	lines, err := ListLines(context.Background(), model.DNSProviderTypeCLOUDFLARE, domains[0].Name, domains[0].ID, []byte(`{"api_token":"secret"}`), client)
	if err != nil || len(lines) != 1 || lines[0].Code != "default" {
		t.Fatalf("lines=%#v err=%v", lines, err)
	}
}
