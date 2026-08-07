import type {
    CachePolicy,
    Certificate,
    ClusterChoice,
    CompressionPolicy,
    DeliveryPolicy,
    DistributionItem,
    MonitoringOverview,
    SecurityPolicy,
    SiteDetails,
    SiteListenerConfig,
    SiteOrigin,
    TrafficPoint,
} from '@/api';
import type { DonutSlice } from '@/components/DonutChart.tsx';

import { Button, Input, TextArea } from '@heroui/react';
import {
    ArrowLeft,
    BarChart3,
    Cloud,
    FileArchive,
    FileText,
    Globe2,
    HardDrive,
    LockKeyhole,
    Plus,
    Rocket,
    Route,
    Save,
    ScrollText,
    Server,
    Settings,
    ShieldCheck,
    Trash2,
} from 'lucide-react';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import {
    ApiError,
    analyticsApi,
    certificatesApi,
    clustersApi,
    dnsApi,
    publishApi,
    sitesApi,
} from '@/api';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DonutChart } from '@/components/DonutChart.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { LoadingSurface } from '@/components/LoadingSurface.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { RankingBars } from '@/components/RankingBars.tsx';
import { SearchableMultiAddField } from '@/components/SearchableMultiAddField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { SettingsActionBar } from '@/components/SettingsActionBar.tsx';
import { SiteCachePurge } from '@/components/SiteCachePurge.tsx';
import { SiteCacheRules } from '@/components/SiteCacheRules.tsx';
import { SiteCacheSettings } from '@/components/SiteCacheSettings.tsx';
import { SiteCompressionSettings } from '@/components/SiteCompressionSettings.tsx';
import { SiteDeliverySettings } from '@/components/SiteDeliverySettings.tsx';
import { SiteDevelopmentMode } from '@/components/SiteDevelopmentMode.tsx';
import { SiteSecuritySettings } from '@/components/SiteSecuritySettings.tsx';
import { TimeSeriesChart } from '@/components/TimeSeriesChart.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { SiteAccessLogsView } from '@/pages/SitesAccessLogs.tsx';
import { defaultDeliveryPolicy, normalizeDeliveryPolicy } from '@/utils/delivery.ts';
import { canManageCluster, canOperateCluster } from '@/utils/rbac.ts';
import { fillTrafficSeries } from '@/utils/timeseries.ts';

type DetailTab = 'overview' | 'logs' | 'settings';
type SettingsPage =
    | 'basic'
    | 'domains'
    | 'http'
    | 'https'
    | 'origins'
    | 'delivery'
    | 'security'
    | 'cache'
    | 'cache-rules'
    | 'compression';
type Period = '24h' | '30d';
type OriginDraft = SiteOrigin & { draft_id: string };

function withSecurityEditorIDs(policy: SecurityPolicy): SecurityPolicy {
    return {
        waf: {
            ...policy.waf,
            rollout_percentage: 100,
            block_response: policy.waf.block_response ?? { type: 'DEFAULT' },
            presets: policy.waf.presets ?? [],
            groups: (policy.waf.groups ?? []).map((group) => ({
                ...group,
                rollout_percentage: group.rollout_percentage ?? 100,
                response: group.response ?? { type: 'DEFAULT' },
                rules: (group.rules ?? []).map((rule) => ({
                    ...rule,
                    id: rule.id ?? crypto.randomUUID(),
                })),
            })),
            exceptions: (policy.waf.exceptions ?? []).map((exception) => ({
                ...exception,
                conditions: {
                    ...exception.conditions,
                    group_operator: exception.conditions?.group_operator ?? 'AND',
                    groups: (exception.conditions?.groups ?? []).map((group) => ({
                        ...group,
                        id: group.id ?? crypto.randomUUID(),
                        rules: (group.rules ?? []).map((rule) => ({
                            ...rule,
                            id: rule.id ?? crypto.randomUUID(),
                        })),
                    })),
                },
            })),
        },
        access: policy.access,
        rate_limit: {
            ...policy.rate_limit,
            rules: (policy.rate_limit.rules ?? []).map((rule) => ({
                ...rule,
                conditions: {
                    ...rule.conditions,
                    group_operator: rule.conditions?.group_operator ?? 'AND',
                    groups: (rule.conditions?.groups ?? []).map((group) => ({
                        ...group,
                        id: group.id ?? crypto.randomUUID(),
                        rules: (group.rules ?? []).map((condition) => ({
                            ...condition,
                            id: condition.id ?? crypto.randomUUID(),
                        })),
                    })),
                },
            })),
        },
    };
}

function loadErrorMessage(error: unknown) {
    return error instanceof ApiError ? error.message : 'request failed';
}

function valuesEqual(left: unknown, right: unknown) {
    return JSON.stringify(left) === JSON.stringify(right);
}

function setsEqual(left: Set<string>, right: Set<string>) {
    return left.size === right.size && Array.from(left).every((value) => right.has(value));
}

function originsEqual(drafts: OriginDraft[], saved: SiteOrigin[]) {
    return valuesEqual(
        drafts.map(({ draft_id: _draftID, ...origin }) => origin),
        saved
    );
}

const tabs = [
    { id: 'overview' as const, label: 'Overview', icon: BarChart3 },
    { id: 'logs' as const, label: 'Logs', icon: ScrollText },
    { id: 'settings' as const, label: 'Settings', icon: Settings },
];
const settingsPages = [
    { id: 'basic' as const, label: 'Basic', icon: Settings },
    { id: 'domains' as const, label: 'Domains', icon: Globe2 },
    { id: 'http' as const, label: 'HTTP', icon: FileText },
    { id: 'https' as const, label: 'HTTPS', icon: LockKeyhole },
    { id: 'origins' as const, label: 'Origins', icon: Server },
    { id: 'delivery' as const, label: 'Delivery', icon: Route },
    { id: 'security' as const, label: 'Security', icon: ShieldCheck },
    { id: 'cache' as const, label: 'Cache', icon: HardDrive },
    { id: 'cache-rules' as const, label: 'Rules', icon: HardDrive },
    { id: 'compression' as const, label: 'Compression', icon: FileArchive },
];
type SettingsNavEntry =
    | { kind: 'page'; id: SettingsPage }
    | { kind: 'divider' }
    | { kind: 'group'; id: SettingsPage; children: { id: SettingsPage; label: string }[] };
const settingsNav: SettingsNavEntry[] = [
    { kind: 'page', id: 'basic' },
    { kind: 'page', id: 'domains' },
    { kind: 'page', id: 'http' },
    { kind: 'page', id: 'https' },
    { kind: 'page', id: 'origins' },
    { kind: 'divider' },
    { kind: 'page', id: 'delivery' },
    { kind: 'page', id: 'security' },
    {
        kind: 'group',
        id: 'cache',
        children: [
            { id: 'cache', label: 'Settings' },
            { id: 'cache-rules', label: 'Rules' },
        ],
    },
    { kind: 'page', id: 'compression' },
];
const palette = ['#2563eb', '#0891b2', '#059669', '#d97706', '#dc2626', '#64748b'];

function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const unit = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatBandwidth(bytesPerSecond: number) {
    const bits = bytesPerSecond * 8;
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
    if (bits <= 0) return '0 bps';
    const unit = Math.min(Math.floor(Math.log(bits) / Math.log(1000)), units.length - 1);
    return `${(bits / 1000 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function trafficOf(item: { ingress_bytes: number; egress_bytes: number }) {
    return item.ingress_bytes + item.egress_bytes;
}

function toSlices(items: DistributionItem[], colorOffset = 0): DonutSlice[] {
    return items.slice(0, 6).map((item, index) => ({
        label: item.value || '(empty)',
        value: item.requests,
        color: palette[(index + colorOffset) % palette.length],
    }));
}

function SectionHeader({ title, description }: { title: string; description: string }) {
    return (
        <div className='border-b border-border px-5 py-4'>
            <h2 className='text-sm font-semibold'>{title}</h2>
            <p className='mt-1 text-xs leading-5 text-muted'>{description}</p>
        </div>
    );
}

function Metric({ label, value, note }: { label: string; value: string; note?: string }) {
    return (
        <div className='min-w-0 border-b border-border px-4 py-4 last:border-b-0 sm:border-b-0 sm:border-r sm:last:border-r-0'>
            <div className='text-xs font-medium text-muted'>{label}</div>
            <div className='mt-1 font-mono text-xl font-semibold tracking-tight'>{value}</div>
            {note && <div className='mt-1 truncate text-xs text-muted'>{note}</div>}
        </div>
    );
}

function RankingTable({
    title,
    label,
    items,
}: {
    title: string;
    label: string;
    items: DistributionItem[];
}) {
    return (
        <ContentCard className='h-full' noPadding>
            <SectionHeader
                title={title}
                description={`Top ${label.toLowerCase()} for the selected period.`}
            />
            <div className='overflow-x-auto'>
                <table className='w-full min-w-[520px] text-left text-sm'>
                    <thead className='bg-surface-secondary/50 text-xs text-muted'>
                        <tr>
                            <th className='px-4 py-2.5'>{label}</th>
                            <th className='px-4 py-2.5 text-right'>Requests</th>
                            <th className='px-4 py-2.5 text-right'>Traffic</th>
                        </tr>
                    </thead>
                    <tbody className='divide-y divide-border'>
                        {items.slice(0, 10).map((item) => (
                            <tr key={item.value}>
                                <td
                                    className='max-w-sm truncate px-4 py-2.5 font-mono text-xs'
                                    title={item.value}
                                >
                                    {item.value || '(empty)'}
                                </td>
                                <td className='px-4 py-2.5 text-right font-mono'>
                                    {item.requests.toLocaleString()}
                                </td>
                                <td className='px-4 py-2.5 text-right text-muted'>
                                    {formatBytes(trafficOf(item))}
                                </td>
                            </tr>
                        ))}
                        {items.length === 0 && (
                            <tr>
                                <td className='px-4 py-10 text-center text-muted' colSpan={3}>
                                    No analytics data
                                </td>
                            </tr>
                        )}
                    </tbody>
                </table>
            </div>
        </ContentCard>
    );
}

export default function SiteDetail() {
    const navigate = useNavigate();
    const { siteId = '', '*': detailPath = '' } = useParams();
    const { clusterId, clusters: availableClusters, ready, setClusterId } = useCluster();
    const clusterRole = availableClusters.find((cluster) => cluster.id === clusterId)?.role;
    const canOperate = canOperateCluster(clusterRole);
    const canManage = canManageCluster(clusterRole);
    const api = useMemo(() => sitesApi(clusterId), [clusterId]);
    const analytics = useMemo(() => analyticsApi(clusterId), [clusterId]);
    const publishing = useMemo(() => publishApi(clusterId), [clusterId]);
    const certificateApi = useMemo(() => certificatesApi(clusterId), [clusterId]);
    const dns = useMemo(() => dnsApi(clusterId), [clusterId]);
    const parts = detailPath.split('/').filter(Boolean);
    const requestedTab = parts[0] || 'overview';
    const tab: DetailTab =
        tabs.some((item) => item.id === requestedTab) &&
        (requestedTab !== 'settings' || !ready || canOperate)
            ? (requestedTab as DetailTab)
            : 'overview';
    const requestedSettings = parts[1] || 'basic';
    const settingsPage: SettingsPage = settingsPages.some((item) => item.id === requestedSettings)
        ? (requestedSettings as SettingsPage)
        : 'basic';
    const canonicalDetailPath = tab === 'settings' ? `settings/${settingsPage}` : tab;
    const visibleTabs = !ready || canOperate ? tabs : tabs.filter((item) => item.id !== 'settings');
    const previousDetailPathRef = useRef(detailPath);

    const [site, setSite] = useState<SiteDetails | null>(null);
    const [deleteSiteOpen, setDeleteSiteOpen] = useState(false);
    const [listener, setListener] = useState<SiteListenerConfig>({});
    const [cache, setCache] = useState<CachePolicy>({});
    const [compression, setCompression] = useState<CompressionPolicy>({});
    const [delivery, setDelivery] = useState<DeliveryPolicy>(defaultDeliveryPolicy);
    const [security, setSecurity] = useState<SecurityPolicy>({
        waf: {
            enabled: true,
            engine: 'GOVETO_COMPAT',
            rule_set_version: '2026.07.1',
            auto_update: true,
            rollout_percentage: 100,
            mode: 'BLOCK',
            block_status: 403,
            block_response: { type: 'DEFAULT' },
            max_body_bytes: 65536,
            presets: [],
            groups: [],
            exceptions: [],
        },
        access: {
            enabled: false,
            mode: 'BLOCK',
            status_code: 403,
            trusted_proxies: [],
            ip_allowlist: [],
            ip_blocklist: [],
            allowed_countries: [],
            blocked_countries: [],
            allowed_regions: [],
            blocked_regions: [],
            allowed_methods: [],
            blocked_methods: [],
            allowed_referer_hosts: [],
            allow_empty_referer: true,
            temporary_blocks: false,
            temporary_block_failure: 'OPEN',
        },
        rate_limit: { enabled: false, backend: 'LOCAL', failure_mode: 'LOCAL', rules: [] },
    });
    const [clusters, setClusters] = useState<ClusterChoice[]>([]);
    const [certificates, setCertificates] = useState<Certificate[]>([]);
    const [certificateIds, setCertificateIds] = useState<Set<string>>(new Set());
    const [cnameTarget, setCnameTarget] = useState('');
    const [overview, setOverview] = useState<MonitoringOverview | null>(null);
    const [traffic24h, setTraffic24h] = useState<TrafficPoint[]>([]);
    const [traffic30d, setTraffic30d] = useState<TrafficPoint[]>([]);
    const [period, setPeriod] = useState<Period>('24h');
    const [domains, setDomains] = useState<DistributionItem[]>([]);
    const [extensions, setExtensions] = useState<DistributionItem[]>([]);
    const [hostnames, setHostnames] = useState<DistributionItem[]>([]);
    const [statuses, setStatuses] = useState<DistributionItem[]>([]);
    const [methods, setMethods] = useState<DistributionItem[]>([]);
    const [paths, setPaths] = useState<DistributionItem[]>([]);
    const [ipsRequests, setIpsRequests] = useState<DistributionItem[]>([]);
    const [ipsTraffic, setIpsTraffic] = useState<DistributionItem[]>([]);
    const [loading, setLoading] = useState(true);
    const [monitoringLoading, setMonitoringLoading] = useState(false);
    const [saving, setSaving] = useState(false);
    const [publishingSite, setPublishingSite] = useState(false);
    const [error, setError] = useState('');
    const [message, setMessage] = useState('');
    const [name, setName] = useState('');
    const [targetCluster, setTargetCluster] = useState('');
    const [domainText, setDomainText] = useState('');
    const [origins, setOrigins] = useState<OriginDraft[]>([]);
    const savedListenerRef = useRef(listener);
    const savedCacheRef = useRef(cache);
    const savedCompressionRef = useRef(compression);
    const savedDeliveryRef = useRef(delivery);
    const savedSecurityRef = useRef(security);

    const loadBase = useCallback(async () => {
        if (!clusterId || !siteId) return;
        setLoading(true);
        try {
            const siteData = await api.get(siteId);
            setSite(siteData);
            setCertificateIds(new Set(siteData.certificate_ids));
            setName(siteData.name);
            setTargetCluster(siteData.cluster_id);
            setDomainText(siteData.domains.join('\n'));
            setOrigins(
                siteData.origins.map((origin, index) => ({
                    ...origin,
                    draft_id: `${siteData.id}-${index}`,
                }))
            );

            const [
                listenerResult,
                cacheResult,
                compressionResult,
                deliveryResult,
                securityResult,
                clusterResult,
                dnsResult,
                certificateResult,
            ] = await Promise.allSettled([
                api.getListener(siteId),
                api.getCache(siteId),
                api.getCompression(siteId),
                api.getDelivery(siteId),
                api.getSecurity(siteId),
                clustersApi.list(),
                dns.config(),
                certificateApi.list(),
            ]);

            const failures: string[] = [];
            if (listenerResult.status === 'fulfilled') {
                savedListenerRef.current = listenerResult.value;
                setListener(listenerResult.value);
            } else failures.push(`listener: ${loadErrorMessage(listenerResult.reason)}`);
            if (cacheResult.status === 'fulfilled') {
                savedCacheRef.current = cacheResult.value;
                setCache(cacheResult.value);
            } else failures.push(`cache: ${loadErrorMessage(cacheResult.reason)}`);
            if (compressionResult.status === 'fulfilled') {
                savedCompressionRef.current = compressionResult.value;
                setCompression(compressionResult.value);
            } else failures.push(`compression: ${loadErrorMessage(compressionResult.reason)}`);
            if (deliveryResult.status === 'fulfilled') {
                const savedDelivery = normalizeDeliveryPolicy(deliveryResult.value);
                savedDeliveryRef.current = savedDelivery;
                setDelivery(savedDelivery);
            } else failures.push(`delivery: ${loadErrorMessage(deliveryResult.reason)}`);
            if (securityResult.status === 'fulfilled') {
                const savedSecurity = withSecurityEditorIDs(securityResult.value);
                savedSecurityRef.current = savedSecurity;
                setSecurity(savedSecurity);
            } else failures.push(`security: ${loadErrorMessage(securityResult.reason)}`);
            if (clusterResult.status === 'fulfilled') setClusters(clusterResult.value.clusters);
            else failures.push(`clusters: ${loadErrorMessage(clusterResult.reason)}`);
            if (dnsResult.status === 'fulfilled')
                setCnameTarget(dnsResult.value.primary_hostname || '');
            else failures.push(`DNS: ${loadErrorMessage(dnsResult.reason)}`);
            if (certificateResult.status === 'fulfilled') setCertificates(certificateResult.value);
            else failures.push(`certificates: ${loadErrorMessage(certificateResult.reason)}`);

            setError(
                failures.length > 0
                    ? `Site loaded, but some settings are unavailable: ${failures.join('; ')}`
                    : ''
            );
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load site');
        } finally {
            setLoading(false);
        }
    }, [api, certificateApi, clusterId, dns, siteId]);

    const loadOverview = useCallback(async () => {
        if (!clusterId || !siteId) return;
        setMonitoringLoading(true);
        const params = { site_id: siteId, period, limit: 10 } as const;
        try {
            const [
                overviewData,
                h24,
                d30,
                domainData,
                extensionData,
                hostnameData,
                statusData,
                methodData,
                pathData,
                ipRequestData,
                ipTrafficData,
            ] = await Promise.all([
                analytics.overview({ site_id: siteId }),
                analytics.traffic({ site_id: siteId, period: '24h' }),
                analytics.traffic({ site_id: siteId, period: '30d' }),
                analytics.rankings('domain', { ...params, sort: 'requests' }),
                analytics.distributions('extension', { ...params, sort: 'requests' }),
                analytics.distributions('hostname', { ...params, sort: 'requests' }),
                analytics.distributions('status', { ...params, sort: 'requests' }),
                analytics.distributions('method', { ...params, sort: 'requests' }),
                analytics.rankings('path', { ...params, sort: 'requests' }),
                analytics.rankings('ip', { ...params, sort: 'requests' }),
                analytics.rankings('ip', { ...params, sort: 'traffic' }),
            ]);
            setOverview(overviewData);
            setTraffic24h(h24.series);
            setTraffic30d(d30.series);
            setDomains(domainData);
            setExtensions(extensionData);
            setHostnames(hostnameData);
            setStatuses(statusData);
            setMethods(methodData);
            setPaths(pathData);
            setIpsRequests(ipRequestData);
            setIpsTraffic(ipTrafficData);
            setError('');
        } catch (loadError) {
            setError(
                loadError instanceof ApiError ? loadError.message : 'Failed to load analytics'
            );
        } finally {
            setMonitoringLoading(false);
        }
    }, [analytics, clusterId, period, siteId]);

    useEffect(() => {
        void loadBase();
    }, [loadBase]);
    const loadSiteOverview = useCallback(async () => {
        if (!siteId) return;
        try {
            const [siteData] = await Promise.all([api.get(siteId), loadOverview()]);
            setSite(siteData);
        } catch (overviewError) {
            setError(
                overviewError instanceof ApiError
                    ? overviewError.message
                    : 'Failed to refresh site overview'
            );
        }
    }, [api, loadOverview, siteId]);

    useAutoRefresh(loadSiteOverview, tab === 'overview' && Boolean(clusterId && siteId));

    useEffect(() => {
        if (!siteId || detailPath === canonicalDetailPath) return;
        navigate(`/sites/${siteId}/${canonicalDetailPath}`, { replace: true });
    }, [canonicalDetailPath, detailPath, navigate, siteId]);

    useLayoutEffect(() => {
        previousDetailPathRef.current = detailPath;
    }, [detailPath]);

    const navigateTo = (nextTab: DetailTab, subpage?: SettingsPage) => {
        const nextPath = `${nextTab}${subpage ? `/${subpage}` : ''}`;
        if (detailPath === nextPath) return;
        navigate(`/sites/${siteId}/${nextPath}`);
    };
    const runSave = async (action: () => Promise<void>, success: string) => {
        setSaving(true);
        setError('');
        setMessage('');
        try {
            await action();
            setMessage(success);
        } catch (saveError) {
            setError(saveError instanceof ApiError ? saveError.message : 'Failed to save settings');
        } finally {
            setSaving(false);
        }
    };
    const updateSite = async (payload: Parameters<typeof api.update>[1]) => {
        const sourceCluster = clusterId;
        const updated = await api.update(siteId, payload);
        if (updated.cluster_id !== sourceCluster) {
            await setClusterId(updated.cluster_id);
        }
        setSite(updated);
        setName(updated.name);
        setTargetCluster(updated.cluster_id);
        setCertificateIds(new Set(updated.certificate_ids));
        setDomainText(updated.domains.join('\n'));
        setOrigins(
            updated.origins.map((origin, index) => ({
                ...origin,
                draft_id: `${updated.id}-${index}`,
            }))
        );
    };

    const saveListener = () =>
        runSave(async () => {
            const result = await api.updateListener(siteId, listener);
            savedListenerRef.current = result.listener;
            setListener(result.listener);
        }, 'Listener settings saved and publishing queued.');
    const saveHTTPS = () =>
        runSave(async () => {
            const [updated, result] = await Promise.all([
                api.update(siteId, { certificate_ids: Array.from(certificateIds) }),
                api.updateListener(siteId, listener),
            ]);
            setSite(updated);
            setCertificateIds(new Set(updated.certificate_ids));
            savedListenerRef.current = result.listener;
            setListener(result.listener);
        }, 'HTTPS settings saved and publishing queued.');
    const saveCache = () =>
        runSave(async () => {
            const result = await api.updateCache(siteId, cache);
            savedCacheRef.current = result.cache;
            setCache(result.cache);
        }, 'Cache settings saved and publishing queued.');
    const toggleDevMode = () => {
        const enabling = !(cache.dev_mode ?? false);
        return runSave(
            async () => {
                const result = await api.updateCache(siteId, {
                    ...savedCacheRef.current,
                    dev_mode: enabling,
                });
                savedCacheRef.current = result.cache;
                setCache((current) => ({ ...current, dev_mode: result.cache.dev_mode }));
            },
            enabling
                ? 'Development mode enabled. All requests bypass the cache.'
                : 'Development mode disabled. Cache behavior restored.'
        );
    };
    const saveCompression = () =>
        runSave(async () => {
            const result = await api.updateCompression(siteId, compression);
            savedCompressionRef.current = result.compression;
            setCompression(result.compression);
        }, 'Compression settings saved and publishing queued.');
    const saveDelivery = () =>
        runSave(async () => {
            const result = await api.updateDelivery(siteId, delivery);
            const savedDelivery = normalizeDeliveryPolicy(result.delivery);
            savedDeliveryRef.current = savedDelivery;
            setDelivery(savedDelivery);
        }, 'Delivery settings saved and publishing queued.');
    const saveSecurity = () =>
        runSave(async () => {
            const result = await api.updateSecurity(siteId, security);
            const savedSecurity = withSecurityEditorIDs({
                waf: result.waf,
                access: result.access,
                rate_limit: result.rate_limit,
            });
            savedSecurityRef.current = savedSecurity;
            setSecurity(savedSecurity);
        }, 'Security settings saved and publishing queued.');
    const publish = async () => {
        setPublishingSite(true);
        setError('');
        try {
            await publishing.enqueueSite(siteId);
            setMessage('Publish queued.');
        } catch (publishError) {
            setError(publishError instanceof ApiError ? publishError.message : 'Failed to publish');
        } finally {
            setPublishingSite(false);
        }
    };
    const deleteSite = async () => {
        if (!site) return;
        await runSave(async () => {
            await api.delete(siteId);
            navigate('/sites');
        }, 'Site deleted.');
    };

    const chartTraffic =
        period === '24h'
            ? fillTrafficSeries(traffic24h, '24h')
            : fillTrafficSeries(traffic30d, '30d');
    const bucketSeconds = period === '24h' ? 3600 : 86400;
    const bandwidthChart = chartTraffic.map((point) => ({
        bucket: point.bucket,
        values: { bandwidth: trafficOf(point) / bucketSeconds },
    }));
    const trafficChart = chartTraffic.map((point) => ({
        bucket: point.bucket,
        values: {
            traffic: point.ingress_bytes + point.egress_bytes,
            cache: point.cache_egress_bytes,
        },
    }));
    const requestChart = chartTraffic.map((point) => ({
        bucket: point.bucket,
        values: { requests: point.requests },
    }));
    const certificateOptions = useMemo(
        () =>
            certificates.map((certificate) => ({
                id: certificate.id,
                name: certificate.name,
                detail: certificate.expires_at
                    ? `Expires ${new Date(certificate.expires_at).toLocaleDateString()}`
                    : undefined,
            })),
        [certificates]
    );
    const basicDirty = Boolean(site && (name !== site.name || targetCluster !== site.cluster_id));
    const domainsDirty = Boolean(site && domainText !== site.domains.join('\n'));
    const originsDirty = Boolean(site && !originsEqual(origins, site.origins));
    const listenerDirty = !valuesEqual(listener, savedListenerRef.current);
    const certificateIdsDirty = Boolean(
        site && !setsEqual(certificateIds, new Set(site.certificate_ids))
    );
    const cacheDirty = !valuesEqual(cache, savedCacheRef.current);
    const compressionDirty = !valuesEqual(compression, savedCompressionRef.current);
    const deliveryDirty = !valuesEqual(delivery, savedDeliveryRef.current);
    const securityDirty = !valuesEqual(security, savedSecurityRef.current);
    const enteringDataTab = previousDetailPathRef.current !== detailPath && tab === 'overview';
    const tabContentLoading =
        enteringDataTab ||
        (tab === 'overview' ? monitoringLoading : tab === 'settings' ? saving : false);

    if (!clusterId) return <FormError message='Select a cluster to view this site.' />;

    return (
        <div className='space-y-6'>
            <nav aria-label='Breadcrumb' className='flex flex-wrap items-center gap-2 text-sm'>
                <button
                    className='text-muted hover:text-foreground'
                    type='button'
                    onClick={() => navigate('/sites')}
                >
                    Sites
                </button>
                <span className='text-muted'>/</span>
                <button
                    className='text-muted hover:text-foreground'
                    type='button'
                    onClick={() => navigateTo('overview')}
                >
                    {site?.name || siteId}
                </button>
                <span className='text-muted'>/</span>
                <span className='font-medium'>{tabs.find((item) => item.id === tab)?.label}</span>
                {tab === 'settings' && (
                    <>
                        <span className='text-muted'>/</span>
                        <span className='font-medium'>
                            {settingsPages.find((item) => item.id === settingsPage)?.label}
                        </span>
                    </>
                )}
            </nav>
            <PageHeader
                actions={
                    <Button variant='ghost' onPress={() => navigate('/sites')}>
                        <ArrowLeft className='mr-1.5 h-4 w-4' />
                        Back to sites
                    </Button>
                }
                subtitle={site?.domains.join(', ') || 'Site delivery and traffic management.'}
                title={site?.name || 'Site details'}
            >
                {canOperate && (
                    <Button isDisabled={publishingSite} onPress={() => void publish()}>
                        <Rocket className='mr-2 h-4 w-4' />
                        {publishingSite ? 'Publishing...' : 'Publish'}
                    </Button>
                )}
            </PageHeader>
            {error && <FormError message={error} />}
            {message && (
                <div className='rounded-lg border border-success/20 bg-success/10 px-4 py-3 text-sm text-success'>
                    {message}
                </div>
            )}
            {loading || !ready ? (
                <ContentCard className='p-10 text-center text-sm text-muted'>
                    Loading site...
                </ContentCard>
            ) : (
                site && (
                    <>
                        <div className='flex w-fit items-center gap-1 overflow-x-auto rounded-xl bg-surface p-1'>
                            {visibleTabs.map((item) => {
                                const Icon = item.icon;
                                return (
                                    <button
                                        className={`flex shrink-0 items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${tab === item.id ? 'bg-surface-secondary shadow-sm' : 'text-muted hover:text-foreground'}`}
                                        key={item.id}
                                        type='button'
                                        onClick={() =>
                                            navigateTo(
                                                item.id,
                                                item.id === 'settings' ? settingsPage : undefined
                                            )
                                        }
                                    >
                                        <Icon className='h-4 w-4' />
                                        {item.label}
                                    </button>
                                );
                            })}
                        </div>
                        <LoadingSurface
                            key={canonicalDetailPath}
                            className='min-h-40'
                            isLoading={tabContentLoading}
                            label={`Loading ${tab}`}
                        >
                            {tab === 'overview' && (
                                <div className='space-y-4'>
                                    <ContentCard noPadding>
                                        <div className='grid sm:grid-cols-2 xl:grid-cols-5'>
                                            <Metric
                                                label='Current bandwidth'
                                                value={formatBandwidth(
                                                    overview?.current_bandwidth_bps || 0
                                                )}
                                                note='Latest hourly average'
                                            />
                                            <Metric
                                                label='Today peak bandwidth'
                                                value={formatBandwidth(
                                                    overview?.today_peak_bandwidth_bps || 0
                                                )}
                                            />
                                            <Metric
                                                label='Month peak bandwidth'
                                                value={formatBandwidth(
                                                    overview?.month_peak_bandwidth_bps || 0
                                                )}
                                            />
                                            <Metric
                                                label='Today unique IPs'
                                                value={(
                                                    overview?.today_unique_ips || 0
                                                ).toLocaleString()}
                                            />
                                            <Metric
                                                label='Today traffic'
                                                value={formatBytes(
                                                    trafficOf(
                                                        overview?.today || {
                                                            ingress_bytes: 0,
                                                            egress_bytes: 0,
                                                        }
                                                    )
                                                )}
                                                note={`${(overview?.today.requests || 0).toLocaleString()} requests`}
                                            />
                                        </div>
                                    </ContentCard>
                                    <div className='flex justify-end'>
                                        <div className='flex rounded-lg bg-surface p-1'>
                                            {(['24h', '30d'] as Period[]).map((value) => (
                                                <button
                                                    className={`rounded-md px-3 py-1.5 text-xs font-semibold ${period === value ? 'bg-surface-secondary shadow-sm' : 'text-muted'}`}
                                                    key={value}
                                                    type='button'
                                                    onClick={() => {
                                                        if (value === period) return;
                                                        setMonitoringLoading(true);
                                                        setPeriod(value);
                                                    }}
                                                >
                                                    {value}
                                                </button>
                                            ))}
                                        </div>
                                    </div>
                                    <div className='grid gap-4 xl:grid-cols-3'>
                                        <ContentCard allowOverflow title='Bandwidth'>
                                            <TimeSeriesChart
                                                ariaLabel={`${period} bandwidth`}
                                                data={bandwidthChart}
                                                series={[
                                                    {
                                                        key: 'bandwidth',
                                                        label: 'Bandwidth',
                                                        color: '#2563eb',
                                                    },
                                                ]}
                                                valueFormatter={formatBandwidth}
                                            />
                                        </ContentCard>
                                        <ContentCard allowOverflow title='Traffic'>
                                            <TimeSeriesChart
                                                ariaLabel={`${period} total and cache traffic`}
                                                data={trafficChart}
                                                series={[
                                                    {
                                                        key: 'traffic',
                                                        label: 'Traffic',
                                                        color: '#2563eb',
                                                    },
                                                    {
                                                        key: 'cache',
                                                        label: 'Cache traffic',
                                                        color: '#059669',
                                                    },
                                                ]}
                                                valueFormatter={formatBytes}
                                            />
                                        </ContentCard>
                                        <ContentCard allowOverflow title='Requests'>
                                            <TimeSeriesChart
                                                ariaLabel={`${period} requests`}
                                                data={requestChart}
                                                series={[
                                                    {
                                                        key: 'requests',
                                                        label: 'Requests',
                                                        color: '#2563eb',
                                                    },
                                                ]}
                                            />
                                        </ContentCard>
                                    </div>
                                    <div className='grid gap-4 xl:grid-cols-[1.15fr_.85fr]'>
                                        <ContentCard title='Domain ranking'>
                                            <RankingBars items={domains} />
                                        </ContentCard>
                                        <RankingTable
                                            title='Request paths'
                                            label='Path'
                                            items={paths}
                                        />
                                    </div>
                                    <div className='grid gap-4 md:grid-cols-2 xl:grid-cols-4'>
                                        {[
                                            ['Suffix composition', toSlices(extensions, 0)],
                                            ['Hostname composition', toSlices(hostnames, 1)],
                                            ['Status codes', toSlices(statuses, 2)],
                                            ['Request methods', toSlices(methods, 3)],
                                        ].map(([title, slices]) => (
                                            <ContentCard
                                                className='h-full'
                                                key={title as string}
                                                title={title as string}
                                            >
                                                <DonutChart
                                                    compact
                                                    ariaLabel={title as string}
                                                    slices={slices as DonutSlice[]}
                                                />
                                            </ContentCard>
                                        ))}
                                    </div>
                                    <div className='grid gap-4 xl:grid-cols-2'>
                                        <RankingTable
                                            title='Unique IPs by requests'
                                            label='Client IP'
                                            items={ipsRequests}
                                        />
                                        <RankingTable
                                            title='Unique IPs by traffic'
                                            label='Client IP'
                                            items={ipsTraffic}
                                        />
                                    </div>
                                </div>
                            )}
                            {tab === 'logs' && <SiteAccessLogsView embeddedSiteId={siteId} />}
                            {tab === 'settings' && (
                                <div className='grid gap-6 lg:grid-cols-[200px_1fr]'>
                                    <nav className='flex flex-col gap-1 lg:sticky lg:top-0 lg:self-start'>
                                        {settingsNav.map((entry) => {
                                            if (entry.kind === 'divider') {
                                                return (
                                                    <div
                                                        className='my-1 border-t border-border'
                                                        key='divider'
                                                    />
                                                );
                                            }
                                            const item = settingsPages.find(
                                                (page) => page.id === entry.id
                                            );
                                            if (!item) return null;
                                            const Icon = item.icon;
                                            const itemClassName = (active: boolean) =>
                                                `flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium transition-colors ${
                                                    active
                                                        ? 'bg-surface-secondary text-foreground'
                                                        : 'text-muted hover:bg-surface-secondary hover:text-foreground'
                                                }`;
                                            if (entry.kind === 'group') {
                                                const expanded = entry.children.some(
                                                    (child) => child.id === settingsPage
                                                );
                                                return (
                                                    <div key={entry.id}>
                                                        <button
                                                            className={itemClassName(
                                                                settingsPage === entry.id
                                                            )}
                                                            type='button'
                                                            onClick={() =>
                                                                navigateTo('settings', entry.id)
                                                            }
                                                        >
                                                            <Icon className='h-4 w-4' />
                                                            {item.label}
                                                        </button>
                                                        {expanded && (
                                                            <div className='ml-5 mt-1 space-y-1 border-l border-border pl-2'>
                                                                {entry.children.map((child) => (
                                                                    <button
                                                                        className={itemClassName(
                                                                            settingsPage ===
                                                                                child.id
                                                                        )}
                                                                        key={child.id}
                                                                        type='button'
                                                                        onClick={() =>
                                                                            navigateTo(
                                                                                'settings',
                                                                                child.id
                                                                            )
                                                                        }
                                                                    >
                                                                        {child.label}
                                                                    </button>
                                                                ))}
                                                            </div>
                                                        )}
                                                    </div>
                                                );
                                            }
                                            return (
                                                <button
                                                    className={itemClassName(
                                                        settingsPage === item.id
                                                    )}
                                                    key={item.id}
                                                    type='button'
                                                    onClick={() => navigateTo('settings', item.id)}
                                                >
                                                    <Icon className='h-4 w-4' />
                                                    {item.label}
                                                </button>
                                            );
                                        })}
                                    </nav>
                                    <div className='min-w-0'>
                                        {settingsPage === 'basic' && (
                                            <div className='space-y-4'>
                                                <ContentCard noPadding>
                                                    <SectionHeader
                                                        title='Basic settings'
                                                        description='Rename or transfer this site.'
                                                    />
                                                    <div className='space-y-5 p-5'>
                                                        <FormField htmlFor='site-name' label='Name'>
                                                            <Input
                                                                id='site-name'
                                                                value={name}
                                                                variant='secondary'
                                                                onChange={(event) =>
                                                                    setName(event.target.value)
                                                                }
                                                            />
                                                        </FormField>
                                                        <FormField
                                                            htmlFor='site-cluster'
                                                            label='Cluster'
                                                            hint={
                                                                site.certificate_count > 0
                                                                    ? 'Remove certificates before transferring this site.'
                                                                    : 'Transferring clears node publish state and republishes in the target cluster.'
                                                            }
                                                        >
                                                            <SelectField
                                                                ariaLabel='Cluster'
                                                                className='w-full'
                                                                isDisabled={
                                                                    !canManage ||
                                                                    site.certificate_count > 0
                                                                }
                                                                id='site-cluster'
                                                                options={clusters.map(
                                                                    (cluster) => ({
                                                                        id: cluster.id,
                                                                        label: cluster.name,
                                                                    })
                                                                )}
                                                                value={targetCluster}
                                                                variant='secondary'
                                                                onChange={setTargetCluster}
                                                            />
                                                        </FormField>
                                                    </div>
                                                </ContentCard>
                                                {canManage && (
                                                    <ContentCard noPadding>
                                                        <div className='border-b border-danger/20 px-5 py-4'>
                                                            <h2 className='text-sm font-semibold text-danger'>
                                                                Delete site
                                                            </h2>
                                                            <p className='mt-1 text-xs text-muted'>
                                                                Permanently removes configuration
                                                                and publish history.
                                                            </p>
                                                        </div>
                                                        <div className='flex justify-end p-5'>
                                                            <Button
                                                                variant='danger'
                                                                onPress={() =>
                                                                    setDeleteSiteOpen(true)
                                                                }
                                                            >
                                                                <Trash2 className='mr-1.5 h-4 w-4' />
                                                                Delete site
                                                            </Button>
                                                        </div>
                                                    </ContentCard>
                                                )}
                                                <SettingsActionBar
                                                    isDirty={basicDirty}
                                                    isDiscardDisabled={saving}
                                                    onDiscard={() => {
                                                        setName(site.name);
                                                        setTargetCluster(site.cluster_id);
                                                    }}
                                                >
                                                    <Button
                                                        isDisabled={saving || !name.trim()}
                                                        onPress={() =>
                                                            void runSave(
                                                                () =>
                                                                    updateSite({
                                                                        name: name.trim(),
                                                                        cluster_id: targetCluster,
                                                                    }),
                                                                'Basic settings saved.'
                                                            )
                                                        }
                                                    >
                                                        <Save className='mr-1.5 h-4 w-4' />
                                                        Save basic settings
                                                    </Button>
                                                </SettingsActionBar>
                                            </div>
                                        )}
                                        {settingsPage === 'domains' && (
                                            <div className='space-y-4'>
                                                <ContentCard noPadding>
                                                    <SectionHeader
                                                        title='Domain configuration'
                                                        description='One hostname per line. DNS should point to the CNAME target below.'
                                                    />
                                                    <div className='space-y-5 p-5'>
                                                        <FormField
                                                            htmlFor='site-domains'
                                                            label='Domains'
                                                        >
                                                            <TextArea
                                                                id='site-domains'
                                                                rows={7}
                                                                value={domainText}
                                                                variant='secondary'
                                                                onChange={(event) =>
                                                                    setDomainText(
                                                                        event.target.value
                                                                    )
                                                                }
                                                            />
                                                        </FormField>
                                                        {cnameTarget && (
                                                            <div className='rounded-lg bg-surface-secondary px-4 py-3'>
                                                                <div className='text-xs font-medium text-muted'>
                                                                    CNAME target
                                                                </div>
                                                                <code className='mt-1 block overflow-x-auto text-sm'>
                                                                    {cnameTarget}
                                                                </code>
                                                            </div>
                                                        )}
                                                    </div>
                                                </ContentCard>
                                                <SettingsActionBar
                                                    isDirty={domainsDirty}
                                                    isDiscardDisabled={saving}
                                                    onDiscard={() =>
                                                        setDomainText(site.domains.join('\n'))
                                                    }
                                                >
                                                    <Button
                                                        isDisabled={saving}
                                                        onPress={() =>
                                                            void runSave(
                                                                () =>
                                                                    updateSite({
                                                                        domains: domainText
                                                                            .split('\n')
                                                                            .map((value) =>
                                                                                value.trim()
                                                                            )
                                                                            .filter(Boolean),
                                                                    }),
                                                                'Domains saved.'
                                                            )
                                                        }
                                                    >
                                                        <Save className='mr-1.5 h-4 w-4' />
                                                        Save domains
                                                    </Button>
                                                </SettingsActionBar>
                                            </div>
                                        )}
                                        {settingsPage === 'http' && (
                                            <div className='space-y-4'>
                                                <ContentCard noPadding>
                                                    <SectionHeader
                                                        title='HTTP configuration'
                                                        description='Control the plain HTTP listener and HTTPS redirect.'
                                                    />
                                                    <div className='space-y-5 p-5'>
                                                        <div className='flex items-center justify-between gap-4'>
                                                            <div>
                                                                <div className='text-sm font-medium'>
                                                                    Enable HTTP
                                                                </div>
                                                                <div className='text-xs text-muted'>
                                                                    Accept requests over plain HTTP.
                                                                </div>
                                                            </div>
                                                            <ToggleSwitch
                                                                label='Enable HTTP'
                                                                isSelected={
                                                                    listener.http_enabled ?? true
                                                                }
                                                                onChange={(value) =>
                                                                    setListener({
                                                                        ...listener,
                                                                        http_enabled: value,
                                                                    })
                                                                }
                                                            />
                                                        </div>
                                                        <FormField
                                                            htmlFor='http-port'
                                                            label='HTTP port'
                                                        >
                                                            <Input
                                                                id='http-port'
                                                                min={1}
                                                                max={65535}
                                                                type='number'
                                                                value={String(
                                                                    listener.http_port ?? 80
                                                                )}
                                                                variant='secondary'
                                                                onChange={(event) =>
                                                                    setListener({
                                                                        ...listener,
                                                                        http_port: Number(
                                                                            event.target.value
                                                                        ),
                                                                    })
                                                                }
                                                            />
                                                        </FormField>
                                                        <div className='flex items-center justify-between gap-4'>
                                                            <div>
                                                                <div className='text-sm font-medium'>
                                                                    Redirect to HTTPS
                                                                </div>
                                                                <div className='text-xs text-muted'>
                                                                    Send HTTP requests to the secure
                                                                    listener.
                                                                </div>
                                                            </div>
                                                            <ToggleSwitch
                                                                label='Redirect to HTTPS'
                                                                isSelected={
                                                                    listener.redirect_http_to_https ??
                                                                    true
                                                                }
                                                                onChange={(value) =>
                                                                    setListener({
                                                                        ...listener,
                                                                        redirect_http_to_https:
                                                                            value,
                                                                    })
                                                                }
                                                            />
                                                        </div>
                                                    </div>
                                                </ContentCard>
                                                <SettingsActionBar
                                                    isDirty={listenerDirty}
                                                    isDiscardDisabled={saving}
                                                    onDiscard={() =>
                                                        setListener(savedListenerRef.current)
                                                    }
                                                >
                                                    <Button
                                                        isDisabled={saving}
                                                        onPress={() => void saveListener()}
                                                    >
                                                        <Save className='mr-1.5 h-4 w-4' />
                                                        Save HTTP settings
                                                    </Button>
                                                </SettingsActionBar>
                                            </div>
                                        )}
                                        {settingsPage === 'https' && (
                                            <div className='space-y-4'>
                                                <ContentCard className='overflow-visible' noPadding>
                                                    <SectionHeader
                                                        title='HTTPS configuration'
                                                        description='Choose certificates and configure the HTTPS listener.'
                                                    />
                                                    <div className='space-y-5 p-5'>
                                                        <FormField
                                                            label='Certificates'
                                                            hint='Select from every certificate available in this cluster.'
                                                        >
                                                            <SearchableMultiAddField
                                                                addLabel='Add certificate'
                                                                dialogTitle='Select certificates'
                                                                emptyLabel='No certificates selected'
                                                                itemLabel='certificate'
                                                                options={certificateOptions}
                                                                searchPlaceholder='Search certificates…'
                                                                selected={certificateIds}
                                                                onChange={setCertificateIds}
                                                            />
                                                        </FormField>
                                                        <div className='flex items-center justify-between gap-4 rounded-xl border border-border/70 bg-surface-secondary/20 px-4 py-3.5'>
                                                            <div>
                                                                <div className='text-sm font-medium'>
                                                                    Enable HTTPS
                                                                </div>
                                                                <div className='mt-0.5 text-xs text-muted'>
                                                                    Serve this site over TLS using
                                                                    the selected certificates.
                                                                </div>
                                                            </div>
                                                            <ToggleSwitch
                                                                label='Enable HTTPS'
                                                                isSelected={Boolean(
                                                                    listener.https_enabled
                                                                )}
                                                                onChange={(https_enabled) =>
                                                                    setListener({
                                                                        ...listener,
                                                                        https_enabled,
                                                                    })
                                                                }
                                                            />
                                                        </div>
                                                        <FormField
                                                            htmlFor='https-port'
                                                            label='HTTPS port'
                                                        >
                                                            <Input
                                                                id='https-port'
                                                                min={1}
                                                                max={65535}
                                                                type='number'
                                                                value={String(
                                                                    listener.https_port ?? 443
                                                                )}
                                                                variant='secondary'
                                                                onChange={(event) =>
                                                                    setListener({
                                                                        ...listener,
                                                                        https_port: Number(
                                                                            event.target.value
                                                                        ),
                                                                    })
                                                                }
                                                            />
                                                        </FormField>
                                                    </div>
                                                </ContentCard>

                                                <ContentCard noPadding>
                                                    <SectionHeader
                                                        title='Advanced HTTPS settings'
                                                        description='Protocol compatibility, transport features, and strict transport security.'
                                                    />
                                                    <div className='space-y-5 p-5'>
                                                        <FormField
                                                            htmlFor='tls-min'
                                                            label='Minimum TLS version'
                                                        >
                                                            <SelectField
                                                                ariaLabel='Minimum TLS version'
                                                                id='tls-min'
                                                                options={[
                                                                    {
                                                                        id: 'TLS1_2',
                                                                        label: 'TLS 1.2',
                                                                    },
                                                                    {
                                                                        id: 'TLS1_3',
                                                                        label: 'TLS 1.3',
                                                                    },
                                                                ]}
                                                                value={
                                                                    listener.tls_min_version ??
                                                                    'TLS1_2'
                                                                }
                                                                variant='secondary'
                                                                onChange={(value) =>
                                                                    setListener({
                                                                        ...listener,
                                                                        tls_min_version: value,
                                                                    })
                                                                }
                                                            />
                                                        </FormField>
                                                        {[
                                                            ['HTTP/2', 'http2_enabled'],
                                                            ['HTTP/3', 'http3_enabled'],
                                                            [
                                                                'OCSP stapling',
                                                                'ocsp_stapling_enabled',
                                                            ],
                                                            ['HSTS', 'hsts_enabled'],
                                                            [
                                                                'HSTS include subdomains',
                                                                'hsts_include_subdomains',
                                                            ],
                                                            ['HSTS preload', 'hsts_preload'],
                                                        ].map(([label, key]) => (
                                                            <div
                                                                className='flex items-center justify-between gap-4'
                                                                key={key}
                                                            >
                                                                <div className='text-sm font-medium'>
                                                                    {label}
                                                                </div>
                                                                <ToggleSwitch
                                                                    label={label}
                                                                    isSelected={Boolean(
                                                                        listener[
                                                                            key as keyof SiteListenerConfig
                                                                        ]
                                                                    )}
                                                                    onChange={(value) =>
                                                                        setListener({
                                                                            ...listener,
                                                                            [key]: value,
                                                                        })
                                                                    }
                                                                />
                                                            </div>
                                                        ))}
                                                        {listener.hsts_enabled && (
                                                            <FormField
                                                                htmlFor='hsts-age'
                                                                label='HSTS max age'
                                                            >
                                                                <Input
                                                                    id='hsts-age'
                                                                    min={0}
                                                                    type='number'
                                                                    value={String(
                                                                        listener.hsts_max_age ??
                                                                            31536000
                                                                    )}
                                                                    variant='secondary'
                                                                    onChange={(event) =>
                                                                        setListener({
                                                                            ...listener,
                                                                            hsts_max_age: Number(
                                                                                event.target.value
                                                                            ),
                                                                        })
                                                                    }
                                                                />
                                                            </FormField>
                                                        )}
                                                    </div>
                                                </ContentCard>
                                                <SettingsActionBar
                                                    isDirty={listenerDirty || certificateIdsDirty}
                                                    isDiscardDisabled={saving}
                                                    onDiscard={() => {
                                                        setListener(savedListenerRef.current);
                                                        setCertificateIds(
                                                            new Set(site.certificate_ids)
                                                        );
                                                    }}
                                                >
                                                    <Button
                                                        isDisabled={saving}
                                                        onPress={() => void saveHTTPS()}
                                                    >
                                                        <ShieldCheck className='mr-1.5 h-4 w-4' />
                                                        Save HTTPS settings
                                                    </Button>
                                                </SettingsActionBar>
                                            </div>
                                        )}
                                        {settingsPage === 'origins' && (
                                            <div className='space-y-4'>
                                                <ContentCard noPadding>
                                                    <SectionHeader
                                                        title='Origin configuration'
                                                        description='Requests are distributed across enabled upstreams by weight.'
                                                    />
                                                    <div className='space-y-4 p-5'>
                                                        {origins.map((origin, index) => (
                                                            <div
                                                                className='grid gap-3 rounded-xl border border-border p-4 md:grid-cols-[110px_1fr_1fr_90px_auto]'
                                                                key={origin.draft_id}
                                                            >
                                                                <SelectField
                                                                    ariaLabel={`Origin ${index + 1} protocol`}
                                                                    options={[
                                                                        {
                                                                            id: 'HTTP',
                                                                            label: 'HTTP',
                                                                        },
                                                                        {
                                                                            id: 'HTTPS',
                                                                            label: 'HTTPS',
                                                                        },
                                                                    ]}
                                                                    value={origin.protocol}
                                                                    variant='secondary'
                                                                    onChange={(value) =>
                                                                        setOrigins(
                                                                            origins.map(
                                                                                (
                                                                                    item,
                                                                                    itemIndex
                                                                                ) =>
                                                                                    itemIndex ===
                                                                                    index
                                                                                        ? {
                                                                                              ...item,
                                                                                              protocol:
                                                                                                  value as SiteOrigin['protocol'],
                                                                                          }
                                                                                        : item
                                                                            )
                                                                        )
                                                                    }
                                                                />
                                                                <Input
                                                                    aria-label={`Origin ${index + 1} address`}
                                                                    placeholder='origin.example.com:443'
                                                                    value={origin.address}
                                                                    variant='secondary'
                                                                    onChange={(event) =>
                                                                        setOrigins(
                                                                            origins.map(
                                                                                (
                                                                                    item,
                                                                                    itemIndex
                                                                                ) =>
                                                                                    itemIndex ===
                                                                                    index
                                                                                        ? {
                                                                                              ...item,
                                                                                              address:
                                                                                                  event
                                                                                                      .target
                                                                                                      .value,
                                                                                          }
                                                                                        : item
                                                                            )
                                                                        )
                                                                    }
                                                                />
                                                                <Input
                                                                    aria-label={`Origin ${index + 1} host header`}
                                                                    placeholder='Host header'
                                                                    value={origin.host_header || ''}
                                                                    variant='secondary'
                                                                    onChange={(event) =>
                                                                        setOrigins(
                                                                            origins.map(
                                                                                (
                                                                                    item,
                                                                                    itemIndex
                                                                                ) =>
                                                                                    itemIndex ===
                                                                                    index
                                                                                        ? {
                                                                                              ...item,
                                                                                              host_header:
                                                                                                  event
                                                                                                      .target
                                                                                                      .value,
                                                                                          }
                                                                                        : item
                                                                            )
                                                                        )
                                                                    }
                                                                />
                                                                <Input
                                                                    aria-label={`Origin ${index + 1} weight`}
                                                                    min={1}
                                                                    type='number'
                                                                    value={String(
                                                                        origin.weight ?? 1
                                                                    )}
                                                                    variant='secondary'
                                                                    onChange={(event) =>
                                                                        setOrigins(
                                                                            origins.map(
                                                                                (
                                                                                    item,
                                                                                    itemIndex
                                                                                ) =>
                                                                                    itemIndex ===
                                                                                    index
                                                                                        ? {
                                                                                              ...item,
                                                                                              weight: Number(
                                                                                                  event
                                                                                                      .target
                                                                                                      .value
                                                                                              ),
                                                                                          }
                                                                                        : item
                                                                            )
                                                                        )
                                                                    }
                                                                />
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label={`Remove origin ${index + 1}`}
                                                                    variant='ghost'
                                                                    onPress={() =>
                                                                        setOrigins(
                                                                            origins.filter(
                                                                                (_, itemIndex) =>
                                                                                    itemIndex !==
                                                                                    index
                                                                            )
                                                                        )
                                                                    }
                                                                >
                                                                    <Trash2 className='h-4 w-4 text-danger' />
                                                                </Button>
                                                            </div>
                                                        ))}
                                                        <Button
                                                            variant='secondary'
                                                            onPress={() =>
                                                                setOrigins([
                                                                    ...origins,
                                                                    {
                                                                        protocol: 'HTTPS',
                                                                        address: '',
                                                                        weight: 1,
                                                                        draft_id:
                                                                            crypto.randomUUID(),
                                                                    },
                                                                ])
                                                            }
                                                        >
                                                            <Plus className='mr-1.5 h-4 w-4' />
                                                            Add origin
                                                        </Button>
                                                    </div>
                                                </ContentCard>
                                                <SettingsActionBar
                                                    isDirty={originsDirty}
                                                    isDiscardDisabled={saving}
                                                    onDiscard={() =>
                                                        setOrigins(
                                                            site.origins.map((origin, index) => ({
                                                                ...origin,
                                                                draft_id: `${site.id}-${index}`,
                                                            }))
                                                        )
                                                    }
                                                >
                                                    <Button
                                                        isDisabled={
                                                            saving ||
                                                            origins.length === 0 ||
                                                            origins.some(
                                                                (origin) => !origin.address.trim()
                                                            )
                                                        }
                                                        onPress={() =>
                                                            void runSave(
                                                                () => updateSite({ origins }),
                                                                'Origins saved.'
                                                            )
                                                        }
                                                    >
                                                        <Cloud className='mr-1.5 h-4 w-4' />
                                                        Save origins
                                                    </Button>
                                                </SettingsActionBar>
                                            </div>
                                        )}
                                        {settingsPage === 'cache' && (
                                            <div className='space-y-4'>
                                                <SiteCachePurge
                                                    site={{
                                                        id: site.id,
                                                        name: site.name,
                                                        domains: site.domains,
                                                    }}
                                                />
                                                <SiteDevelopmentMode
                                                    disabled={saving || !canOperate}
                                                    enabled={cache.dev_mode ?? false}
                                                    onToggle={() => void toggleDevMode()}
                                                />
                                                <SiteCacheSettings
                                                    cache={cache}
                                                    isDirty={cacheDirty}
                                                    saving={saving}
                                                    onChange={setCache}
                                                    onDiscard={() =>
                                                        setCache(savedCacheRef.current)
                                                    }
                                                    onSave={() => void saveCache()}
                                                />
                                            </div>
                                        )}
                                        {settingsPage === 'cache-rules' && (
                                            <SiteCacheRules
                                                cache={cache}
                                                isDirty={cacheDirty}
                                                saving={saving}
                                                onChange={setCache}
                                                onDiscard={() => setCache(savedCacheRef.current)}
                                                onSave={() => void saveCache()}
                                            />
                                        )}
                                        {settingsPage === 'compression' && (
                                            <SiteCompressionSettings
                                                compression={compression}
                                                isDirty={compressionDirty}
                                                saving={saving}
                                                onChange={setCompression}
                                                onDiscard={() =>
                                                    setCompression(savedCompressionRef.current)
                                                }
                                                onSave={() => void saveCompression()}
                                            />
                                        )}
                                        {settingsPage === 'delivery' && (
                                            <SiteDeliverySettings
                                                isDirty={deliveryDirty}
                                                policy={delivery}
                                                saving={saving}
                                                onChange={setDelivery}
                                                onDiscard={() =>
                                                    setDelivery(savedDeliveryRef.current)
                                                }
                                                onSave={() => void saveDelivery()}
                                            />
                                        )}
                                        {settingsPage === 'security' && (
                                            <SiteSecuritySettings
                                                isDirty={securityDirty}
                                                policy={security}
                                                saving={saving}
                                                onChange={setSecurity}
                                                onDiscard={() =>
                                                    setSecurity(savedSecurityRef.current)
                                                }
                                                onSave={() => void saveSecurity()}
                                            />
                                        )}
                                    </div>
                                </div>
                            )}
                        </LoadingSurface>
                    </>
                )
            )}
            <ConfirmDialog
                danger
                confirmLabel='Delete site'
                description={`Delete site "${site?.name ?? ''}" and all of its configuration?`}
                isOpen={deleteSiteOpen}
                title='Delete site?'
                onConfirm={() => {
                    setDeleteSiteOpen(false);
                    void deleteSite();
                }}
                onOpenChange={setDeleteSiteOpen}
            />
        </div>
    );
}
