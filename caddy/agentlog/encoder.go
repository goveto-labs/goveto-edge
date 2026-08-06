package agentlog

import (
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/caddyserver/caddy/v2"
	caddylogging "github.com/caddyserver/caddy/v2/modules/logging"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

// AccessEncoder is a purpose-built JSON encoder for Goveto access logs.
// It applies the privacy floor while fields are still structured.
type AccessEncoder struct {
	zapcore.Encoder `json:"-"`
	caddylogging.LogEncoderConfig
	RedactedHeaders []string `json:"redacted_headers,omitempty"`
}

var benchmarkAccessLogsEnabled atomic.Bool
var emptyBufferPool = buffer.NewPool()

func init() {
	benchmarkAccessLogsEnabled.Store(true)
	caddy.RegisterModule(AccessEncoder{})
}

// SetBenchmarkAccessLogsEnabled is used only by the private benchmark listener.
func SetBenchmarkAccessLogsEnabled(enabled bool) {
	benchmarkAccessLogsEnabled.Store(enabled)
}

func (AccessEncoder) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "caddy.logging.encoders.goveto_access", New: func() caddy.Module { return new(AccessEncoder) }}
}

func (encoder *AccessEncoder) Provision(caddy.Context) error {
	headers := make(map[string]struct{}, len(encoder.RedactedHeaders))
	for _, header := range encoder.RedactedHeaders {
		if header = http.CanonicalHeaderKey(strings.TrimSpace(header)); header != "" {
			headers[header] = struct{}{}
		}
	}
	encoder.Encoder = accessPrivacyEncoder{
		Encoder:         zapcore.NewJSONEncoder(encoder.ZapcoreEncoderConfig()),
		redactedHeaders: headers,
	}
	return nil
}

type accessPrivacyEncoder struct {
	zapcore.Encoder
	context         accessContext
	redactedHeaders map[string]struct{}
}

type accessContext uint8

const (
	accessContextRoot accessContext = iota
	accessContextRequest
	accessContextRequestHeaders
	accessContextResponseHeaders
	accessContextOther
)

func (encoder accessPrivacyEncoder) AddObject(key string, marshaler zapcore.ObjectMarshaler) error {
	switch {
	case encoder.context == accessContextRoot && key == "request":
		encoder.context = accessContextRequest
	case encoder.context == accessContextRoot && key == "resp_headers":
		encoder.context = accessContextResponseHeaders
	case encoder.context == accessContextRequest && key == "headers":
		encoder.context = accessContextRequestHeaders
	default:
		encoder.context = accessContextOther
	}
	return encoder.Encoder.AddObject(key, accessObjectMarshaler{encoder: encoder, marshaler: marshaler})
}

func (encoder accessPrivacyEncoder) AddString(key, value string) {
	if encoder.context == accessContextRequest {
		switch key {
		case "uri":
			value, _, _ = strings.Cut(value, "?")
		case "client_ip", "remote_ip":
			value = maskIP(value)
		}
	}
	encoder.Encoder.AddString(key, value)
}

func (encoder accessPrivacyEncoder) AddArray(key string, marshaler zapcore.ArrayMarshaler) error {
	if encoder.context == accessContextRequestHeaders || encoder.context == accessContextResponseHeaders {
		if _, redact := encoder.redactedHeaders[http.CanonicalHeaderKey(key)]; redact {
			return encoder.Encoder.AddArray(key, zapcore.ArrayMarshalerFunc(func(array zapcore.ArrayEncoder) error {
				array.AppendString("[REDACTED]")
				return nil
			}))
		}
	}
	return encoder.Encoder.AddArray(key, marshaler)
}

func (encoder accessPrivacyEncoder) Clone() zapcore.Encoder {
	return accessPrivacyEncoder{Encoder: encoder.Encoder.Clone(), context: encoder.context, redactedHeaders: encoder.redactedHeaders}
}

func (encoder accessPrivacyEncoder) EncodeEntry(entry zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	if !benchmarkAccessLogsEnabled.Load() {
		return emptyBufferPool.Get(), nil
	}
	encoder.Encoder = encoder.Encoder.Clone()
	for _, field := range fields {
		field.AddTo(encoder)
	}
	return encoder.Encoder.EncodeEntry(entry, nil)
}

type accessObjectMarshaler struct {
	encoder   accessPrivacyEncoder
	marshaler zapcore.ObjectMarshaler
}

func (marshaler accessObjectMarshaler) MarshalLogObject(zapcore.ObjectEncoder) error {
	return marshaler.marshaler.MarshalLogObject(marshaler.encoder)
}

func maskIP(value string) string {
	var ip netip.Addr
	if address, err := netip.ParseAddrPort(value); err == nil {
		ip = address.Addr()
	} else {
		ip, err = netip.ParseAddr(strings.Trim(value, "[]"))
		if err != nil {
			return value
		}
	}
	ip = ip.Unmap()
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(ip, bits).Masked().Addr().String()
}

var (
	_ caddy.Provisioner       = (*AccessEncoder)(nil)
	_ zapcore.Encoder         = (*AccessEncoder)(nil)
	_ zapcore.ObjectMarshaler = (*accessObjectMarshaler)(nil)
)
