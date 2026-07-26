package analytics

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type caddyAccess struct {
	TS      float64 `json:"ts"`
	Request struct {
		RemoteIP string      `json:"remote_ip"`
		ClientIP string      `json:"client_ip"`
		Proto    string      `json:"proto"`
		Method   string      `json:"method"`
		Host     string      `json:"host"`
		URI      string      `json:"uri"`
		Headers  http.Header `json:"headers"`
	} `json:"request"`
	BytesRead   uint64      `json:"bytes_read"`
	Duration    float64     `json:"duration"`
	Size        uint64      `json:"size"`
	Status      uint16      `json:"status"`
	RespHeaders http.Header `json:"resp_headers"`
}

func ParseAccess(payload []byte, clusterID, nodeID, siteID string) (WebRequestLog, error) {
	var raw caddyAccess
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebRequestLog{}, err
	}

	u, err := url.ParseRequestURI(raw.Request.URI)
	if err != nil {
		return WebRequestLog{}, err
	}

	host, _, err := net.SplitHostPort(raw.Request.Host)
	if err != nil {
		host = raw.Request.Host
	}

	ipText := raw.Request.ClientIP
	if ipText == "" {
		ipText = raw.Request.RemoteIP
	}

	ip, _ := netip.ParseAddr(strings.Trim(ipText, "[]"))
	if !ip.IsValid() {
		ip = netip.IPv6Unspecified()
	}
	if ip.Is4() {
		ip = netip.AddrFrom16(ip.As16())
	}

	cacheStatus := raw.RespHeaders.Get("X-Cache")
	if cacheStatus == "" {
		cacheStatus = raw.RespHeaders.Get("Cache-Status")
	}

	eventTime := time.Unix(0, int64(raw.TS*float64(time.Second))).UTC()
	if raw.TS == 0 {
		eventTime = time.Now().UTC()
	}

	return WebRequestLog{
		EventTime:           eventTime,
		RequestID:           raw.Request.Headers.Get("X-Request-Id"),
		ClusterID:           clusterID,
		NodeID:              nodeID,
		SiteID:              siteID,
		Hostname:            strings.ToLower(host),
		Method:              raw.Request.Method,
		Scheme:              scheme(raw.Request.Headers),
		Protocol:            raw.Request.Proto,
		Path:                u.Path,
		QueryString:         u.RawQuery,
		ClientIP:            ip,
		StatusCode:          raw.Status,
		RequestHeaderBytes:  headerBytes(raw.Request.Headers),
		RequestBodyBytes:    raw.BytesRead,
		ResponseHeaderBytes: headerBytes(raw.RespHeaders),
		ResponseBodyBytes:   raw.Size,
		Duration:            time.Duration(raw.Duration * float64(time.Second)),
		CacheStatus:         normalizeCache(cacheStatus),
		ContentType:         raw.RespHeaders.Get("Content-Type"),
		FileExtension:       strings.TrimPrefix(strings.ToLower(filepath.Ext(u.Path)), "."),
		Referer:             raw.Request.Headers.Get("Referer"),
		UserAgent:           raw.Request.Headers.Get("User-Agent"),
		WAFAction:           headerValue(raw.RespHeaders, "X-Goveto-WAF"),
		WAFRuleID:           headerValue(raw.RespHeaders, "X-Goveto-WAF-Rule"),
		WAFSource:           headerValue(raw.RespHeaders, "X-Goveto-WAF-Source"),
		WAFMatch:            headerValue(raw.RespHeaders, "X-Goveto-WAF-Match"),
		WAFTags:             headerValue(raw.RespHeaders, "X-Goveto-WAF-Tag"),
	}, nil
}

func headerValue(headers http.Header, name string) string {
	for candidate, values := range headers {
		if strings.EqualFold(candidate, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func headerBytes(headers http.Header) uint64 {
	var size uint64
	for name, values := range headers {
		for _, value := range values {
			size += uint64(len(name) + len(value) + 4)
		}
	}
	return size
}

func scheme(h http.Header) string {
	if h.Get("X-Forwarded-Proto") != "" {
		return h.Get("X-Forwarded-Proto")
	}
	return "http"
}

func normalizeCache(v string) string {
	v = strings.ToUpper(v)
	for _, x := range []string{"STALE", "HIT", "MISS", "BYPASS"} {
		if strings.Contains(v, x) {
			return x
		}
	}
	return "BYPASS"
}
