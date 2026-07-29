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
	var event map[string]json.RawMessage
	if json.Unmarshal(payload, &event) != nil {
		return payload, true
	}
	var request map[string]json.RawMessage
	if rawRequest, ok := event["request"]; ok && json.Unmarshal(rawRequest, &request) != nil {
		return payload, true
	}
	changed := false
	if p.RedactQuery {
		if rawURI, ok := request["uri"]; ok {
			var uri string
			if json.Unmarshal(rawURI, &uri) != nil {
				uri = ""
			}
			if parsed, err := url.ParseRequestURI(uri); err == nil {
				parsed.RawQuery = ""
				parsed.ForceQuery = false
				redactedURI, _ := json.Marshal(parsed.RequestURI())
				request["uri"] = redactedURI
				changed = changed || uri != parsed.RequestURI()
			}
		}
	}
	if p.AnonymizeIP {
		for _, field := range []string{"client_ip", "remote_ip"} {
			if rawValue, ok := request[field]; ok {
				var value string
				if json.Unmarshal(rawValue, &value) == nil {
					anonymized := anonymizeIP(value)
					if anonymized != value {
						request[field], _ = json.Marshal(anonymized)
						changed = true
					}
				}
			}
		}
	}
	if rawHeaders, ok := request["headers"]; ok {
		redacted, headerChanged := redactHeaders(rawHeaders, p.RedactedHeaders)
		if headerChanged {
			request["headers"] = redacted
			changed = true
		}
	}
	if rawHeaders, ok := event["resp_headers"]; ok {
		redacted, headerChanged := redactHeaders(rawHeaders, p.RedactedHeaders)
		if headerChanged {
			event["resp_headers"] = redacted
			changed = true
		}
	}
	if !changed {
		return payload, true
	}
	if request != nil {
		redactedRequest, err := json.Marshal(request)
		if err != nil {
			return payload, true
		}
		event["request"] = redactedRequest
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
	ip = ip.Unmap()
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(ip, bits).Masked().Addr().String()
}

func redactHeaders(payload json.RawMessage, redacted map[string]struct{}) (json.RawMessage, bool) {
	var headers map[string]json.RawMessage
	if json.Unmarshal(payload, &headers) != nil {
		return payload, false
	}
	changed := false
	for name := range headers {
		if _, ok := redacted[strings.ToLower(name)]; ok {
			headers[name] = json.RawMessage(`["[REDACTED]"]`)
			changed = true
		}
	}
	if !changed {
		return payload, false
	}
	result, err := json.Marshal(headers)
	if err != nil {
		return payload, false
	}
	return result, true
}
