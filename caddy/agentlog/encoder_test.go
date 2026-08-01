package agentlog

import (
	"encoding/json"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type testRequestLog struct{}

func (testRequestLog) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("uri", "/asset?token=secret")
	encoder.AddString("client_ip", "192.0.2.129")
	encoder.AddString("remote_ip", "2001:db8:1:2::99")
	return encoder.AddObject("headers", testHeaders{"Authorization": "secret", "X-Trace": "keep"})
}

type testHeaders map[string]string

func (headers testHeaders) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	for name, value := range headers {
		if err := encoder.AddArray(name, zapcore.ArrayMarshalerFunc(func(array zapcore.ArrayEncoder) error {
			array.AppendString(value)
			return nil
		})); err != nil {
			return err
		}
	}
	return nil
}

func TestAccessEncoderSanitizesStructuredFields(t *testing.T) {
	encoder := &AccessEncoder{RedactedHeaders: []string{"authorization"}}
	if err := encoder.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	encoded, err := encoder.EncodeEntry(zapcore.Entry{}, []zapcore.Field{zap.Object("request", testRequestLog{})})
	if err != nil {
		t.Fatal(err)
	}
	defer encoded.Free()
	var event struct {
		Request struct {
			URI      string              `json:"uri"`
			ClientIP string              `json:"client_ip"`
			RemoteIP string              `json:"remote_ip"`
			Headers  map[string][]string `json:"headers"`
		} `json:"request"`
	}
	if err = json.Unmarshal(encoded.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Request.URI != "/asset" || event.Request.ClientIP != "192.0.2.0" || event.Request.RemoteIP != "2001:db8:1::" {
		t.Fatalf("request was not sanitized: %+v", event.Request)
	}
	if got := event.Request.Headers["Authorization"]; len(got) != 1 || got[0] != "[REDACTED]" {
		t.Fatalf("authorization=%v", got)
	}
	if got := event.Request.Headers["X-Trace"]; len(got) != 1 || got[0] != "keep" {
		t.Fatalf("x-trace=%v", got)
	}
}

func BenchmarkAccessEncoder(b *testing.B) {
	encoder := &AccessEncoder{RedactedHeaders: []string{"authorization"}}
	if err := encoder.Provision(caddy.Context{}); err != nil {
		b.Fatal(err)
	}
	fields := []zapcore.Field{zap.Object("request", testRequestLog{})}
	b.ReportAllocs()
	for range b.N {
		encoded, err := encoder.EncodeEntry(zapcore.Entry{}, fields)
		if err != nil {
			b.Fatal(err)
		}
		encoded.Free()
	}
}

func TestAccessEncoderAllocationBudget(t *testing.T) {
	encoder := &AccessEncoder{RedactedHeaders: []string{"authorization"}}
	if err := encoder.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	fields := []zapcore.Field{zap.Object("request", testRequestLog{})}
	allocations := testing.AllocsPerRun(100, func() {
		encoded, err := encoder.EncodeEntry(zapcore.Entry{}, fields)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Free()
	})
	if allocations > 24 {
		t.Fatalf("access encoder allocations/op=%.0f, want at most 24", allocations)
	}
}
