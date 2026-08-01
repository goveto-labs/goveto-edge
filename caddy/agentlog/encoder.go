package agentlog

import (
	"net"
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
		if header = strings.ToLower(strings.TrimSpace(header)); header != "" {
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
	prefix          string
	redactedHeaders map[string]struct{}
}

func (encoder accessPrivacyEncoder) AddObject(key string, marshaler zapcore.ObjectMarshaler) error {
	encoder.prefix += key + ">"
	return encoder.Encoder.AddObject(key, accessObjectMarshaler{encoder: encoder, marshaler: marshaler})
}

func (encoder accessPrivacyEncoder) AddString(key, value string) {
	switch encoder.prefix + key {
	case "request>uri":
		value, _, _ = strings.Cut(value, "?")
	case "request>client_ip", "request>remote_ip":
		value = maskIP(value)
	}
	encoder.Encoder.AddString(key, value)
}

func (encoder accessPrivacyEncoder) AddArray(key string, marshaler zapcore.ArrayMarshaler) error {
	path := encoder.prefix + key
	if strings.HasPrefix(path, "request>headers>") || strings.HasPrefix(path, "resp_headers>") {
		if _, redact := encoder.redactedHeaders[strings.ToLower(key)]; redact {
			return encoder.Encoder.AddArray(key, zapcore.ArrayMarshalerFunc(func(array zapcore.ArrayEncoder) error {
				array.AppendString("[REDACTED]")
				return nil
			}))
		}
	}
	return encoder.Encoder.AddArray(key, marshaler)
}

func (encoder accessPrivacyEncoder) Clone() zapcore.Encoder {
	return accessPrivacyEncoder{Encoder: encoder.Encoder.Clone(), prefix: encoder.prefix, redactedHeaders: encoder.redactedHeaders}
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
	host := strings.Trim(value, "[]")
	if parsedHost, _, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return value
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
