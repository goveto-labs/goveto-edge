package edgeagent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type LogPolicy struct {
	SampleRate      float64
	RedactQuery     bool
	AnonymizeIP     bool
	RedactedHeaders map[string]struct{}
}

func logPolicyFromEnv() LogPolicy {
	rate := 1.0
	if value := strings.TrimSpace(envOr("EDGE_AGENT_LOG_SAMPLE_RATE", "1")); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed >= 0 && parsed <= 1 {
			rate = parsed
		}
	}
	headers := envOr(
		"EDGE_AGENT_LOG_REDACT_HEADERS",
		"authorization,cf-connecting-ip,cookie,forwarded,proxy-authorization,set-cookie,true-client-ip,x-api-key,x-forwarded-for,x-real-ip",
	)
	redactedHeaders := map[string]struct{}{}
	for header := range strings.SplitSeq(headers, ",") {
		header = strings.ToLower(strings.TrimSpace(header))
		if header != "" {
			redactedHeaders[header] = struct{}{}
		}
	}
	return LogPolicy{
		SampleRate: rate, RedactQuery: envBool("EDGE_AGENT_LOG_REDACT_QUERY", true),
		AnonymizeIP: envBool("EDGE_AGENT_LOG_ANONYMIZE_IP", true), RedactedHeaders: redactedHeaders,
	}
}

func (p LogPolicy) Apply(payload []byte) ([]byte, bool) {
	if p.SampleRate <= 0 || p.SampleRate < 1 && !sampled(payload, p.SampleRate) {
		return nil, false
	}
	if !p.RedactQuery && !p.AnonymizeIP && len(p.RedactedHeaders) == 0 {
		return payload, true
	}
	var event map[string]any
	if json.Unmarshal(payload, &event) != nil {
		return payload, true
	}
	request, _ := event["request"].(map[string]any)
	if request == nil {
		return payload, true
	}
	if p.RedactQuery {
		if rawURI, ok := request["uri"].(string); ok {
			if parsed, err := url.ParseRequestURI(rawURI); err == nil {
				parsed.RawQuery = ""
				parsed.ForceQuery = false
				request["uri"] = parsed.RequestURI()
			}
		}
	}
	if p.AnonymizeIP {
		for _, field := range []string{"client_ip", "remote_ip"} {
			if value, ok := request[field].(string); ok {
				request[field] = anonymizeIP(value)
			}
		}
	}
	if headers, ok := request["headers"].(map[string]any); ok {
		redactHeaders(headers, p.RedactedHeaders)
	}
	if headers, ok := event["resp_headers"].(map[string]any); ok {
		redactHeaders(headers, p.RedactedHeaders)
	}
	redacted, err := json.Marshal(event)
	if err != nil {
		return payload, true
	}
	return redacted, true
}

func sampled(payload []byte, rate float64) bool {
	digest := sha256.Sum256(payload)
	value := binary.BigEndian.Uint64(digest[:8])
	threshold := uint64(rate * float64(^uint64(0)))
	return value <= threshold
}

func anonymizeIP(value string) string {
	host := strings.Trim(value, "[]")
	if parsedHost, _, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return value
	}
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(ip, bits).Masked().Addr().String()
}

func redactHeaders(headers map[string]any, redacted map[string]struct{}) {
	for name := range headers {
		if _, ok := redacted[strings.ToLower(name)]; ok {
			headers[name] = []any{"[REDACTED]"}
		}
	}
}
