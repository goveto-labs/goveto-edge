export interface User {
    id: string;
    email: string;
    name: string;
    role: string;
    status: string;
    totp_enabled?: boolean;
    totp_required?: boolean;
    is_instance_owner: boolean;
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
    agent_gateway_public_address: string;
}

export interface AdminSettings {
    agent_gateway_public_address: string;
    restart_required: boolean;
    restarting: boolean;
}

export interface UpdateAdminSettings {
    agent_gateway_public_address: string;
    restart: boolean;
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

export interface TOTPSetup {
    secret: string;
    uri: string;
}

export interface TOTPMutationRequest {
    password: string;
    code: string;
    secret?: string;
}

export interface RecoveryCodesResponse {
    recovery_codes: string[];
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

export type ClusterRole = 'VIEWER' | 'OPERATOR' | 'OWNER' | 'ADMIN';

export interface ClusterChoice {
    id: string;
    name: string;
    role: ClusterRole;
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

export type SSHAuthType = 'PASSWORD' | 'PRIVATE_KEY';

export interface SSHCredential {
    id: string;
    name: string;
    username: string;
    auth_type: SSHAuthType;
    node_count: number;
    created_at: string;
    updated_at: string;
}

export interface SSHCredentialNode {
    id: string;
    name: string;
    status: string;
}

export interface SSHCredentialWriteRequest {
    name: string;
    username: string;
    auth_type: SSHAuthType;
    password?: string;
    private_key?: string;
    passphrase?: string;
}

export interface NodeSSH {
    entry_ip: string;
    port: number;
    credential_id: string;
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
    sshCredentialId?: string;
    sshHost?: string;
    sshPort?: number;
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
    identity_json?: string;
    identity_available: boolean;
    service_unit: string;
    architectures: string[];
}

export interface NodeRequestLog {
    event_time: string;
    source_log_id: number;
    request_id: string;
    node_id: string;
    config_version: number;
    hostname: string;
    method: string;
    scheme: string;
    protocol: string;
    path: string;
    query_string: string;
    client_ip: string;
    country?: string;
    region?: string;
    status_code: number;
    request_header_bytes: number;
    request_body_bytes: number;
    response_header_bytes: number;
    response_body_bytes: number;
    duration_us: number;
    upstream_address: string;
    upstream_status: number;
    handler_error?: string;
    cache_status: string;
    content_type: string;
    file_extension: string;
    referer: string;
    user_agent: string;
    waf_action?: string;
    waf_rule_id?: string;
    waf_source?: string;
    waf_match?: string;
    waf_tags?: string;
}

export interface RequestLogPage {
    items: NodeRequestLog[];
    page: number;
    page_size: number;
    total: number;
}

export interface WAFRuleStat {
    rule_id: string;
    action: string;
    source: string;
    match: string;
    requests: number;
    unique_ips: number;
    last_seen: string;
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
    bandwidth_bps: number;
    qps: number;
    version: number;
    updated_at: string;
}
export interface SiteDetails extends SiteSummary {
    cluster_id: string;
    certificate_ids: string[];
    origins: SiteOrigin[];
    created_at: string;
}
export interface UpdateSiteRequest {
    name?: string;
    cluster_id?: string;
    certificate_ids?: string[];
    domains?: string[];
    origins?: SiteOrigin[];
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
export interface SiteCompressionUpdateResponse {
    compression: CompressionPolicy;
    publish_job?: PublishJob;
    publish_error?: string;
}

export interface HeaderRule {
    operation: 'SET' | 'ADD' | 'DELETE';
    name: string;
    value?: string;
}

export interface DeliveryOrigin {
    protocol: 'http' | 'https';
    address: string;
    host_header?: string;
    weight?: number;
}

export interface PathOriginPool {
    name: string;
    paths: string[];
    scheduler?: string;
    origins: DeliveryOrigin[];
}

export interface TrafficSplitRule {
    name: string;
    pool: string;
    header_name?: string;
    cookie_name?: string;
    value?: string;
    percentage?: number;
}

export interface DeliveryPolicy {
    request_headers: HeaderRule[];
    response_headers: HeaderRule[];
    rewrites: Array<{ path: string; replacement: string }>;
    redirects: Array<{ path: string; location: string; status: number }>;
    cors: {
        enabled: boolean;
        allow_origins: string[];
        allow_methods: string[];
        allow_headers: string[];
        expose_headers: string[];
        allow_credentials: boolean;
        max_age_seconds: number;
    };
    protocols: { websocket: boolean; grpc: boolean; http_upgrade: boolean };
    error_pages: Array<{ statuses: number[]; content_type?: string; body: string }>;
    origin_prefix: string;
    origin_pools: PathOriginPool[];
    splits: TrafficSplitRule[];
    maintenance: { enabled: boolean; status: number; content_type: string; body: string };
}

export interface SiteDeliveryUpdateResponse {
    delivery: DeliveryPolicy;
    publish_job?: PublishJob;
    publish_error?: string;
}

export interface SiteTemplate {
    id: string;
    name: string;
    config?: SiteBundle;
    updated_at: string;
}

export interface SiteBundle {
    schema_version: number;
    name: string;
    status: string;
    domains: string[];
    certificate_ids?: string[];
    origins: SiteOrigin[];
    [key: string]: unknown;
}

export interface BulkSiteResult {
    site_id: string;
    ok: boolean;
    error?: string;
}

export interface WAFRequestRule {
    id?: string;
    field: string;
    name?: string;
    operator: string;
    value?: string;
    values?: string[];
    negate?: boolean;
    case_sensitive?: boolean;
}

export interface WAFRuleGroup {
    id: string;
    name: string;
    enabled: boolean;
    rollout_percentage: number;
    operator: string;
    action: string;
    status_code?: number;
    response?: WAFResponse;
    redirect_url?: string;
    redirect_status?: number;
    tag?: string;
    rules: WAFRequestRule[];
}

export interface WAFException {
    id: string;
    enabled: boolean;
    rule_ids: string[];
    conditions: RequestConditions;
}

export interface WAFResponse {
    type: 'DEFAULT' | 'HTML' | 'TEXT' | 'JSON';
    body?: string;
}

export interface RequestConditionGroup {
    id?: string;
    operator: string;
    rules: WAFRequestRule[];
}

export interface RequestConditions {
    group_operator: string;
    groups: RequestConditionGroup[];
}

export interface RateLimitRule {
    id: string;
    name: string;
    enabled: boolean;
    key: string;
    key_name?: string;
    requests: number;
    window_seconds: number;
    burst: number;
    ban_seconds: number;
    status_code: number;
    conditions: RequestConditions;
}

export interface SecurityPolicy {
    waf: {
        enabled: boolean;
        engine: string;
        rule_set_version: string;
        auto_update: boolean;
        rollout_percentage: number;
        mode: string;
        block_status: number;
        block_response: WAFResponse;
        max_body_bytes: number;
        presets: string[];
        groups: WAFRuleGroup[];
        exceptions: WAFException[];
    };
    access: {
        enabled: boolean;
        mode: string;
        status_code: number;
        trusted_proxies: string[];
        ip_allowlist: string[];
        ip_blocklist: string[];
        allowed_countries: string[];
        blocked_countries: string[];
        allowed_regions: string[];
        blocked_regions: string[];
        allowed_methods: string[];
        blocked_methods: string[];
        allowed_referer_hosts: string[];
        allow_empty_referer: boolean;
        temporary_blocks: boolean;
        temporary_block_failure: string;
    };
    rate_limit: {
        enabled: boolean;
        backend: string;
        failure_mode: string;
        rules: RateLimitRule[];
    };
}

export interface SiteSecurityUpdateResponse extends SecurityPolicy {
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
    request_coalescing?: boolean;
    cache_range_requests?: boolean;
    max_body_bytes?: number;
    stale?: {
        enabled?: boolean;
        if_error_seconds?: number;
        while_revalidate_seconds?: number;
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

export interface CompressionPolicy {
    enabled?: boolean;
    extensions?: string[];
    excluded_extensions?: string[];
    mime_types?: string[];
    recompress?: boolean;
    minimum_length?: number;
    maximum_length?: number;
    excluded_paths?: string[];
}

export interface Certificate {
    id: string;
    name: string;
    source: 'MANUAL' | 'ACME';
    status:
        | 'PENDING'
        | 'ACTIVE'
        | 'DEPLOYING'
        | 'EXPIRING'
        | 'EXPIRED'
        | 'RENEWAL_FAILED'
        | 'DEPLOYMENT_FAILED';
    fingerprint?: string;
    serial_number?: string;
    domains: string[];
    not_before?: string;
    expires_at?: string;
    issuer?: string;
    key_algorithm?: string;
    acme_directory_url?: string;
    acme_email?: string;
    acme_challenge_type?: 'HTTP_01' | 'DNS_01';
    auto_renew: boolean;
    renew_before_days: number;
    last_issued_at?: string;
    last_renewal_attempt_at?: string;
    last_renewal_error?: string;
    last_published_at?: string;
    last_publish_error?: string;
    created_at: string;
    updated_at?: string;
}

export interface CreateCertificateRequest {
    name: string;
    certificate: string;
    private_key: string;
}

export interface CreateACMECertificateRequest {
    name: string;
    domains: string[];
    email: string;
    directory_url?: string;
    challenge_type: 'HTTP_01' | 'DNS_01';
    auto_renew: boolean;
    renew_before_days: number;
}

export interface CertificateJob {
    id: string;
    certificate_id: string;
    operation: 'ISSUE' | 'RENEW' | 'REISSUE' | 'REPUBLISH';
    status: string;
    attempts: number;
    max_attempts: number;
    next_attempt_at: string;
    error?: string;
    created_at: string;
    updated_at: string;
}

export interface CreateACMECertificateResponse {
    certificate: Certificate;
    job: CertificateJob;
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
    attempts?: number;
    max_attempts?: number;
    next_attempt_at?: string;
    lease_owner?: string;
    lease_until?: string;
    heartbeat_at?: string;
    cancel_requested_at?: string;
    error?: string;
    created_at: string;
    updated_at?: string;
}

export type ManagedJobKind = 'PUBLISH' | 'PURGE' | 'INSTALL' | 'DNS' | 'CERTIFICATE';

export interface ManagedJob {
    id: string;
    kind: ManagedJobKind;
    resource_id: string;
    resource_type: 'SITE' | 'NODE' | 'CLUSTER' | 'CERTIFICATE';
    resource_name: string;
    resource_hint?: string;
    operation: string;
    status: string;
    attempts: number;
    max_attempts: number;
    next_attempt_at: string;
    lease_owner?: string;
    lease_until?: string;
    heartbeat_at?: string;
    cancel_requested_at?: string;
    timeout_at?: string;
    result_json?: unknown;
    compensation_json?: unknown;
    input_json?: unknown;
    error?: string;
    created_at: string;
    updated_at: string;
}

export interface ManagedJobPage {
    items: ManagedJob[];
    page: number;
    page_size: number;
    total: number;
}

export interface JobExecution {
    id: string;
    attempt: number;
    worker_id: string;
    status: string;
    started_at: string;
    heartbeat_at: string;
    finished_at?: string;
    result_json?: unknown;
    error?: string;
}

export type PurgeType = 'URL' | 'PREFIX' | 'TAG' | 'ALL';

export interface PurgeJob {
    id: string;
    site_id: string;
    type: PurgeType;
    value?: string;
    status: string;
    attempts?: number;
    max_attempts?: number;
    next_attempt_at?: string;
    lease_owner?: string;
    lease_until?: string;
    heartbeat_at?: string;
    cancel_requested_at?: string;
    error?: string;
    created_at: string;
    updated_at?: string;
}

export interface PrewarmResult {
    url: string;
    status_code?: number;
    success: boolean;
    error?: string;
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
    cache_directory: string;
    cache_entries: number;
    cache_hits: number;
    cache_misses: number;
    cache_stale_hits: number;
    cache_evictions: number;
    cache_rejected_writes: number;
    cache_corruptions: number;
    cache_hit_rate: number;
    cache_capacity_ratio: number;
    cache_alerts: string[];
    disk_used_bytes: number;
    disk_total_bytes: number;
}

export interface NodeSnapshot extends NodeRuntimePoint {
    online: boolean;
    ingress_bytes_per_second: number;
    egress_bytes_per_second: number;
    cache_egress_bytes_per_second: number;
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
    cache_egress_bytes: number;
}

export interface MonitoringOverview {
    today: UsageTotal;
    yesterday: UsageTotal;
    month: UsageTotal;
    current_bandwidth_bps: number;
    today_peak_bandwidth_bps: number;
    month_peak_bandwidth_bps: number;
    today_unique_ips: number;
}
