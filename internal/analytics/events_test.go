package analytics

import "testing"

func TestWebRequestLogTrafficBytes(t *testing.T) {
	event := WebRequestLog{
		RequestHeaderBytes:  100,
		RequestBodyBytes:    200,
		ResponseHeaderBytes: 300,
		ResponseBodyBytes:   400,
	}
	if got := event.TrafficBytes(); got != 1000 {
		t.Fatalf("unexpected traffic bytes: %d", got)
	}
}
