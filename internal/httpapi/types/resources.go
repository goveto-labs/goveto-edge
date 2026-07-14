package types

import (
	"time"

	"goveto-edge/internal/storage/gen/model"
)

type ClusterGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func NewClusterGroup(value *model.ClusterGroup) ClusterGroup {
	return ClusterGroup{ID: value.Id, Name: value.Name}
}

type ClusterRegion struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func NewClusterRegion(value *model.ClusterRegion) ClusterRegion {
	return ClusterRegion{ID: value.Id, Name: value.Name}
}

type ClusterMember struct {
	ClusterID  string                  `json:"cluster_id"`
	UserID     string                  `json:"user_id"`
	Permission model.ClusterPermission `json:"permission"`
	CreatedAt  time.Time               `json:"created_at"`
}

func NewClusterMember(value *model.ClusterMember) ClusterMember {
	return ClusterMember{ClusterID: value.ClusterId, UserID: value.UserId, Permission: value.Permission, CreatedAt: value.CreatedAt}
}

type Certificate struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func NewCertificate(value *model.Certificate) Certificate {
	return Certificate{ID: value.Id, Name: value.Name, Fingerprint: value.Fingerprint, ExpiresAt: value.ExpiresAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type DNSRecord struct {
	ID               string                `json:"id"`
	Hostname         string                `json:"hostname"`
	Type             model.DNSRecordType   `json:"type"`
	Value            string                `json:"value"`
	Status           model.DNSRecordStatus `json:"status"`
	DNSLineID        *string               `json:"dnsLineId"`
	DNSLineKey       string                `json:"dnsLineKey"`
	ProviderRecordID *string               `json:"providerRecordId"`
	LastError        *string               `json:"lastError"`
	LastSyncedAt     *time.Time            `json:"lastSyncedAt"`
}

func NewDNSRecord(value *model.DNSManagedRecord) DNSRecord {
	return DNSRecord{ID: value.Id, Hostname: value.Hostname, Type: value.Type, Value: value.Value, Status: value.Status, DNSLineID: value.DnsLineId, DNSLineKey: value.DnsLineKey, ProviderRecordID: value.ProviderRecordId, LastError: value.LastError, LastSyncedAt: value.LastSyncedAt}
}

type NodeAddress struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Primary bool   `json:"primary"`
}

func NewNodeAddress(value *model.NodeAddress) NodeAddress {
	return NodeAddress{ID: value.Id, Address: value.Address, Primary: value.Primary}
}

type NodeDNSLine struct {
	NodeID    string `json:"nodeId"`
	DNSLineID string `json:"dnsLineId"`
}
type SiteConfigVersion struct {
	SiteID  string             `json:"site_id"`
	Version int64              `json:"version"`
	Status  model.ConfigStatus `json:"status"`
}

type NodeCacheConfig struct {
	CacheDirectory      string `json:"cache_directory"`
	AutoMaxSize         bool   `json:"auto_max_size"`
	MaxSizeBytes        uint64 `json:"max_size_bytes"`
	MaxDiskUsagePercent int    `json:"max_disk_usage_percent"`
}

func NewNodeCacheConfig(value *model.NodeCacheConfig) NodeCacheConfig {
	result := NodeCacheConfig{CacheDirectory: value.CacheDir, AutoMaxSize: value.AutoMaxSize, MaxDiskUsagePercent: value.MaxDiskUsagePercent}
	if value.MaxSizeBytes != nil {
		result.MaxSizeBytes = uint64(*value.MaxSizeBytes)
	}
	return result
}

type Node struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Status             model.NodeStatus    `json:"status"`
	Addresses          []NodeAddress       `json:"addresses"`
	DNSLines           []NodeDNSLine       `json:"dnsLines,omitempty"`
	SiteConfigVersions []SiteConfigVersion `json:"siteConfigVersions,omitempty"`
	CacheConfig        *NodeCacheConfig    `json:"cacheConfig,omitempty"`
}

func NewNode(value *model.Node) Node {
	result := Node{ID: value.Id, Name: value.Name, Status: value.Status, Addresses: make([]NodeAddress, len(value.Addresses)), DNSLines: make([]NodeDNSLine, len(value.DnsLines)), SiteConfigVersions: make([]SiteConfigVersion, len(value.SiteConfigVersions))}
	for index, item := range value.Addresses {
		result.Addresses[index] = NewNodeAddress(item)
	}
	for index, item := range value.DnsLines {
		result.DNSLines[index] = NodeDNSLine{NodeID: item.NodeId, DNSLineID: item.DnsLineId}
	}
	for index, item := range value.SiteConfigVersions {
		result.SiteConfigVersions[index] = SiteConfigVersion{SiteID: item.SiteId, Version: item.Version, Status: item.Status}
	}
	if value.CacheConfig != nil {
		cache := NewNodeCacheConfig(value.CacheConfig)
		result.CacheConfig = &cache
	}
	return result
}

type SiteListener struct {
	HTTPEnabled           bool                `json:"http_enabled"`
	HTTPPort              int                 `json:"http_port"`
	RedirectHTTPToHTTPS   bool                `json:"redirect_http_to_https"`
	HTTPSEnabled          bool                `json:"https_enabled"`
	HTTPSPort             int                 `json:"https_port"`
	HTTP2Enabled          bool                `json:"http2_enabled"`
	HTTP3Enabled          bool                `json:"http3_enabled"`
	TLSMinVersion         model.TLSMinVersion `json:"tls_min_version"`
	HSTSEnabled           bool                `json:"hsts_enabled"`
	HSTSMaxAge            int                 `json:"hsts_max_age"`
	HSTSIncludeSubdomains bool                `json:"hsts_include_subdomains"`
	HSTSPreload           bool                `json:"hsts_preload"`
	OCSPStaplingEnabled   bool                `json:"ocsp_stapling_enabled"`
}

func NewSiteListener(value *model.SiteListenerConfig) SiteListener {
	return SiteListener{HTTPEnabled: value.HttpEnabled, HTTPPort: value.HttpPort, RedirectHTTPToHTTPS: value.RedirectHttpToHttps, HTTPSEnabled: value.HttpsEnabled, HTTPSPort: value.HttpsPort, HTTP2Enabled: value.Http2Enabled, HTTP3Enabled: value.Http3Enabled, TLSMinVersion: value.TlsMinVersion, HSTSEnabled: value.HstsEnabled, HSTSMaxAge: value.HstsMaxAge, HSTSIncludeSubdomains: value.HstsIncludeSubdomains, HSTSPreload: value.HstsPreload, OCSPStaplingEnabled: value.OcspStaplingEnabled}
}

type PublishJob struct {
	ID        string          `json:"id"`
	SiteID    string          `json:"site_id"`
	Version   int64           `json:"version"`
	Status    model.JobStatus `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func NewPublishJob(value *model.PublishJob) PublishJob {
	return PublishJob{ID: value.Id, SiteID: value.SiteId, Version: value.Version, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type PurgeJob struct {
	ID        string          `json:"id"`
	SiteID    string          `json:"site_id"`
	Type      model.PurgeType `json:"type"`
	Value     *string         `json:"value"`
	Status    model.JobStatus `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func NewPurgeJob(value *model.PurgeJob) PurgeJob {
	return PurgeJob{ID: value.Id, SiteID: value.SiteId, Type: value.Type, Value: value.Value, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}
