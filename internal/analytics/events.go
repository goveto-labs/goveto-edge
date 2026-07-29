package analytics

import (
	"net/netip"
	"time"
)

// WebRequestLog is the canonical first-hand HTTP analytics event. It stores
// byte counts but never request or response body content.
type WebRequestLog struct {
	EventTime           time.Time
	SourceLogID         uint64
	RequestID           string
	ClusterID           string
	NodeID              string
	SiteID              string
	ConfigVersion       uint64
	Hostname            string
	Method              string
	Scheme              string
	Protocol            string
	Path                string
	QueryString         string
	ClientIP            netip.Addr
	StatusCode          uint16
	RequestHeaderBytes  uint64
	RequestBodyBytes    uint64
	ResponseHeaderBytes uint64
	ResponseBodyBytes   uint64
	Duration            time.Duration
	UpstreamAddress     string
	UpstreamStatus      uint16
	CacheStatus         string
	ContentType         string
	FileExtension       string
	Referer             string
	UserAgent           string
	Country             string
	Region              string
	WAFAction           string
	WAFRuleID           string
	WAFSource           string
	WAFMatch            string
	WAFTags             string
}

func (e WebRequestLog) IngressBytes() uint64 {
	return e.RequestHeaderBytes + e.RequestBodyBytes
}

func (e WebRequestLog) EgressBytes() uint64 {
	return e.ResponseHeaderBytes + e.ResponseBodyBytes
}

func (e WebRequestLog) TrafficBytes() uint64 {
	return e.IngressBytes() + e.EgressBytes()
}

const (
	DimensionMethod        = "method"
	DimensionStatusCode    = "status_code"
	DimensionProtocol      = "protocol"
	DimensionCacheStatus   = "cache_status"
	DimensionFileExtension = "file_extension"
	DimensionContentType   = "content_type"
	DimensionCountry       = "country"
	DimensionRegion        = "region"
	DimensionPath          = "path"
	DimensionClientIP      = "client_ip"
	DimensionHostname      = "hostname"
	DimensionReferer       = "referer"
)
