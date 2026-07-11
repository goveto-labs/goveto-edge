package analytics

import "testing"

func TestParseAccess(t *testing.T) {
	payload := []byte(`{
		"ts": 1710000000.25,
		"request": {
			"remote_ip": "192.0.2.1",
			"proto": "HTTP/2.0",
			"method": "GET",
			"host": "example.com",
			"uri": "/assets/app.js?v=1",
			"headers": {
				"User-Agent": ["test"],
				"X-Request-Id": ["req-1"]
			}
		},
		"duration": 0.125,
		"size": 512,
		"status": 200,
		"resp_headers": {
			"X-Cache": ["HIT"],
			"Content-Type": ["application/javascript"]
		}
	}`)
	event, err := ParseAccess(payload, "cluster", "node", "site")
	if err != nil {
		t.Fatal(err)
	}
	if event.Path != "/assets/app.js" || event.QueryString != "v=1" || event.FileExtension != "js" {
		t.Fatalf("unexpected URL fields: %#v", event)
	}
	if event.CacheStatus != "HIT" || event.ClientIP.String() != "::ffff:192.0.2.1" || event.ResponseBodyBytes != 512 {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event.RequestHeaderBytes == 0 || event.ResponseHeaderBytes == 0 {
		t.Fatal("header traffic was not counted")
	}
}
