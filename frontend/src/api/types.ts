export interface User {
    id: string;
    email: string;
    name: string;
    role: string;
    status: string;
}

export interface LoginRequest {
    email: string;
    password: string;
    code?: string;
}

export interface RegisterRequest {
    email: string;
    password: string;
    name: string;
    captcha_token: string;
}

export interface InitializeRequest {
    email: string;
    password: string;
    name: string;
}

export interface InitializationStatus {
    initialized: boolean;
}

export interface RegistrationConfig {
    enabled: boolean;
    captcha?: {
        provider: string;
        site_key: string;
    };
}

export interface DNSLine {
    id: string;
    name: string;
    providerCode: string;
}

export type DNSProviderType = 'ALIYUN' | 'CLOUDFLARE';

export interface DNSProviderDomain {
    name: string;
    id?: string;
}

export interface DNSConfigResponse {
    primary_hostname: string | null;
    provider: {
        type: DNSProviderType;
        zone: string;
        zone_id: string | null;
        default_ttl: number;
        proxied: boolean;
        enabled: boolean;
        credentials_configured: boolean;
    } | null;
}

export interface DNSManagedRecord {
    id: string;
    hostname: string;
    type: 'A' | 'AAAA' | 'CNAME';
    value: string;
    status: string;
    dnsLineId: string | null;
    dnsLineKey: string;
    providerRecordId: string | null;
    lastError: string | null;
    lastSyncedAt: string | null;
}

export interface DNSSyncJob {
    id: string;
    action: string;
    status: string;
    attempts: number;
    maxAttempts: number;
    resultJson: { error?: string } | null;
    createdAt: string;
}

export interface ClusterChoice {
    id: string;
    name: string;
    role: string;
    created_at: string;
}

export interface ClusterListResponse {
    clusters: ClusterChoice[];
    selected_cluster_id: string;
    requires_cluster: boolean;
}

export interface ClusterGroup {
    id: string;
    name: string;
}

export interface ClusterRegion {
    id: string;
    name: string;
}

export interface ClusterMember {
    cluster_id: string;
    user_id: string;
    permission: string;
    created_at: string;
}

export interface NodeAddress {
    id: string;
    address: string;
}

export interface NodeSSH {
    entry_ip: string;
    port: number;
    user: string;
    password?: string;
    private_key?: string;
    passphrase?: string;
}

export interface SSHConnectionTestResponse {
    ok: boolean;
    architecture: string;
}

export interface NodeCacheConfig {
    cache_directory: string;
    auto_max_size: boolean;
    max_size_bytes: number;
    max_disk_usage_percent: number;
}

export interface NodeHardwareProfile {
    architecture: string;
    cpu_model: string;
    cache_disk_write_bytes_per_second?: number;
    benchmark_bytes?: number;
    benchmark_duration_ms?: number;
    measured_at: string;
}

export interface SiteConfigVersion {
    site_id: string;
    version: number;
    status: string;
}

export interface Node {
    id: string;
    name: string;
    status: string;
    version?: string;
    heartbeatAt?: string;
    createdAt: string;
    updatedAt: string;
    installError?: string;
    addresses: NodeAddress[];
    dnsLines?: Array<{ nodeId: string; dnsLineId: string }>;
    groupMemberships?: Array<{ nodeId: string; groupId: string }>;
    regionMemberships?: Array<{ nodeId: string; regionId: string }>;
    siteConfigVersions?: SiteConfigVersion[];
    cacheConfig?: NodeCacheConfig;
    hardwareProfile?: NodeHardwareProfile;
}

export interface NodeStatusResponse {
    id: string;
    status: string;
    message?: string;
}
export interface NodeDNSLinesResponse {
    node_id: string;
    dns_line_ids: string[];
}
export interface NodeCacheUpdateResponse {
    cache_config: NodeCacheConfig;
    synced: boolean;
    sync_error?: string;
}

export interface NodeInstallationInfo {
    node_id: string;
    status: string;
    install_error?: string;
    communication_key: string;
    identity_json: string;
    service_unit: string;
    architectures: string[];
}

export interface NodeRequestLog {
    event_time: string;
    request_id: string;
    hostname: string;
    method: string;
    path: string;
    status_code: number;
    duration_us: number;
    upstream_address: string;
    cache_status: string;
}

export interface CreateNodeRequest {
    name: string;
    addresses: string[];
    dns_line_ids: string[];
    group_ids: string[];
    region_ids: string[];
    ssh: NodeSSH;
}

export interface SiteOrigin {
    protocol: 'HTTP' | 'HTTPS';
    address: string;
    host_header?: string;
    weight?: number;
}

export interface Site {
    id: string;
    name: string;
    domains: string[];
    certificate_ids: string[];
    origins: SiteOrigin[];
    status: string;
    publish_job?: PublishJob;
    publish_error?: string;
}

export interface SiteCreateResponse {
    id: string;
    name: string;
    status: string;
    publish_job?: PublishJob;
    publish_error?: string;
}
export interface SiteSummary {
    id: string;
    name: string;
    status: string;
    domains: string[];
    certificate_count: number;
    version: number;
    updated_at: string;
}
export interface SiteListenerUpdateResponse {
    listener: SiteListenerConfig;
    publish_job?: PublishJob;
    publish_error?: string;
}
export interface SiteCacheUpdateResponse {
    cache: CachePolicy;
    publish_job?: PublishJob;
    publish_error?: string;
}

export interface CreateSiteRequest {
    name: string;
    domains: string[];
    certificate_ids: string[];
    origins: SiteOrigin[];
}

export interface SiteListenerConfig {
    http_enabled?: boolean;
    http_port?: number;
    redirect_http_to_https?: boolean;
    https_enabled?: boolean;
    https_port?: number;
    http2_enabled?: boolean;
    http3_enabled?: boolean;
    tls_min_version?: string;
    hsts_enabled?: boolean;
    hsts_max_age?: number;
    hsts_include_subdomains?: boolean;
    hsts_preload?: boolean;
    ocsp_stapling_enabled?: boolean;
}

export interface CachePolicy {
    enabled?: boolean;
    response_headers?: {
        x_cache?: boolean;
        age?: boolean;
    };
    allow_purge_method?: boolean;
    stale?: {
        enabled?: boolean;
        if_error_seconds?: number;
    };
    ttl?: {
        default_seconds?: number;
        status?: Record<string, number>;
    };
    vary_headers?: string[];
    surrogate_key_header?: string;
    conditions?: {
        group_operator?: string;
        groups?: Array<{
            operator?: string;
            rules?: Array<{
                type?: string;
                value?: string;
                values?: string[];
            }>;
        }>;
    };
}

export interface Certificate {
    id: string;
    name: string;
    cert_pem?: string;
    private_key_pem?: string;
    fingerprint?: string;
    expires_at?: string;
    created_at: string;
    updated_at?: string;
}

export interface CreateCertificateRequest {
    name: string;
    certificate: string;
    private_key: string;
}

export interface PublishTask {
    id: string;
    site_id?: string;
    node_id?: string;
    status: string;
    created_at: string;
    updated_at?: string;
    error?: string;
}

export interface PublishStatus {
    state: string;
    has_active_tasks: boolean;
    has_failed_tasks: boolean;
    pending_count: number;
    running_count: number;
    failed_count: number;
    recent_tasks: PublishTask[];
}

export interface PublishJob {
    id: string;
    site_id: string;
    version?: number;
    status: string;
    created_at: string;
    updated_at?: string;
    publish_error?: string;
}

export type PurgeType = 'URL' | 'PREFIX' | 'TAG' | 'ALL';

export interface PurgeJob {
    id: string;
    site_id: string;
    type: PurgeType;
    value?: string;
    status: string;
    created_at: string;
    updated_at?: string;
}

export interface Summary {
    requests: number;
    ingress_bytes: number;
    egress_bytes: number;
    cache_hits: number;
    cache_misses: number;
    hit_rate: number;
}

export interface TopItem {
    value: string;
    requests: number;
    traffic_bytes: number;
}

export interface TrafficPoint {
    bucket: string;
    requests: number;
    ingress_bytes: number;
    egress_bytes: number;
    cache_egress_bytes: number;
}

export interface TrafficResponse {
    period: string;
    granularity: string;
    series: TrafficPoint[];
}

export interface DistributionItem {
    value: string;
    requests: number;
    ingress_bytes: number;
    egress_bytes: number;
}

export interface NodeRuntimePoint {
    bucket: string;
    node_id: string;
    cpu_usage_percent: number;
    memory_used_bytes: number;
    memory_total_bytes: number;
    load_1: number;
    load_5: number;
    load_15: number;
    connections: number;
    cache_used_bytes: number;
    cache_max_bytes: number;
    cache_directory: string;
    disk_used_bytes: number;
    disk_total_bytes: number;
}

export interface NodeSnapshot extends NodeRuntimePoint {
    online: boolean;
    ingress_bytes_per_second: number;
    egress_bytes_per_second: number;
    requests_per_minute: number;
}

export interface NodeRuntimeResponse {
    period: string;
    series: NodeRuntimePoint[];
}

export interface AnalyticsParams {
    site_id?: string;
    node_id?: string;
    from?: string;
    to?: string;
}

export interface UsageTotal {
    requests: number;
    ingress_bytes: number;
    egress_bytes: number;
}

export interface MonitoringOverview {
    today: UsageTotal;
    yesterday: UsageTotal;
    month: UsageTotal;
}
