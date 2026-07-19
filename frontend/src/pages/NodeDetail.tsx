import type {
    ClusterGroup,
    ClusterRegion,
    DistributionItem,
    DNSLine,
    Node,
    NodeCacheConfig,
    NodeInstallationInfo,
    NodeRequestLog,
    NodeRuntimePoint,
    NodeSnapshot,
    NodeSSH,
    TrafficPoint,
} from '@/api';
import type { DonutSlice } from '@/components/DonutChart.tsx';

import { Button, Input, TextArea } from '@heroui/react';
import {
    ArrowLeft,
    Check,
    Download,
    FileText,
    Globe2,
    HardDrive,
    KeyRound,
    LockKeyhole,
    Network,
    Pencil,
    Plus,
    RefreshCw,
    Save,
    ScrollText,
    Server,
    Settings,
    Terminal,
    Trash2,
    X,
} from 'lucide-react';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import { ApiError, analyticsApi, clusterApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DonutChart } from '@/components/DonutChart.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { FormRow } from '@/components/FormRow.tsx';
import { LoadingSurface } from '@/components/LoadingSurface.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { RankingBars } from '@/components/RankingBars.tsx';
import { SearchableMultiAddField } from '@/components/SearchableMultiAddField.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { TimeSeriesChart } from '@/components/TimeSeriesChart.tsx';
import { useCluster } from '@/hooks/useCluster.ts';
import { fillTrafficSeries } from '@/utils/timeseries.ts';

type DetailTab = 'overview' | 'details' | 'logs' | 'installation' | 'settings';
type SettingsPage = 'network' | 'cache';
type SSHAuthMethod = 'password' | 'private_key';
const bytesPerGB = 1024 ** 3;

function bytesToGB(value: number) {
    if (value === 0) return '0';
    return String(Number((value / bytesPerGB).toFixed(3)));
}

const tabs: Array<{ id: DetailTab; label: string; icon: typeof Server }> = [
    { id: 'overview', label: 'Overview', icon: Server },
    { id: 'details', label: 'Node details', icon: FileText },
    { id: 'logs', label: 'Runtime logs', icon: ScrollText },
    { id: 'installation', label: 'Installation', icon: Terminal },
    { id: 'settings', label: 'Settings', icon: Settings },
];
function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const unit = Math.min(
        Math.max(0, Math.floor(Math.log(bytes) / Math.log(1024))),
        units.length - 1
    );
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatRate(bytesPerSecond: number) {
    return `${formatBytes(bytesPerSecond)}/s`;
}

function SectionTitle({
    icon: Icon,
    title,
    description,
}: {
    icon: typeof Server;
    title: string;
    description: string;
}) {
    return (
        <div className='flex items-start gap-3 border-b border-border px-5 py-4'>
            <span className='flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-surface-secondary text-muted'>
                <Icon className='h-4 w-4' />
            </span>
            <div>
                <h2 className='text-sm font-semibold'>{title}</h2>
                <p className='mt-0.5 text-xs leading-5 text-muted'>{description}</p>
            </div>
        </div>
    );
}

function StatCell({
    label,
    value,
    footer,
    tone = 'default',
}: {
    label: string;
    value: React.ReactNode;
    footer?: React.ReactNode;
    tone?: 'default' | 'success' | 'danger';
}) {
    const toneClass = tone === 'success' ? 'text-success' : tone === 'danger' ? 'text-danger' : '';
    return (
        <div className='min-w-0 rounded-xl border border-border/70 bg-surface p-3.5 shadow-sm'>
            <div className='text-xs font-medium text-muted'>{label}</div>
            <div className={`mt-1 text-lg font-semibold tracking-tight ${toneClass}`}>{value}</div>
            {footer && <div className='mt-1 truncate text-xs text-muted'>{footer}</div>}
        </div>
    );
}

function NameChips({ names, empty }: { names: string[]; empty: string }) {
    if (names.length === 0) return <span className='text-sm text-muted'>{empty}</span>;
    return (
        <div className='flex flex-wrap gap-2'>
            {names.map((name) => (
                <span
                    className='rounded-full border border-border bg-surface-secondary px-2.5 py-1 text-xs font-medium'
                    key={name}
                >
                    {name}
                </span>
            ))}
        </div>
    );
}

function SettingsNavigation({
    page,
    onChange,
}: {
    page: SettingsPage;
    onChange: (page: SettingsPage) => void;
}) {
    return (
        <nav className='flex flex-col gap-1'>
            {[
                { id: 'network' as const, label: 'Network & DNS', icon: Network },
                { id: 'cache' as const, label: 'Cache', icon: HardDrive },
            ].map((item) => {
                const Icon = item.icon;
                return (
                    <button
                        className={`flex items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium transition-colors ${
                            page === item.id
                                ? 'bg-surface-secondary text-foreground'
                                : 'text-muted hover:bg-surface-secondary hover:text-foreground'
                        }`}
                        key={item.id}
                        type='button'
                        onClick={() => onChange(item.id)}
                    >
                        <Icon className='h-4 w-4' />
                        {item.label}
                    </button>
                );
            })}
        </nav>
    );
}

const slicePalette = [
    '#3b82f6',
    '#10b981',
    '#f59e0b',
    '#8b5cf6',
    '#06b6d4',
    '#ec4899',
    '#84cc16',
    '#f97316',
    '#64748b',
];

const methodColors: Record<string, string> = {
    GET: '#3b82f6',
    POST: '#10b981',
    PUT: '#f59e0b',
    DELETE: '#ef4444',
    PATCH: '#8b5cf6',
    HEAD: '#64748b',
    OPTIONS: '#06b6d4',
};

function statusColor(value: string) {
    if (value.startsWith('2')) return '#10b981';
    if (value.startsWith('3')) return '#3b82f6';
    if (value.startsWith('4')) return '#f59e0b';
    if (value.startsWith('5')) return '#ef4444';
    return '#64748b';
}

function toSlices(
    items: DistributionItem[],
    colorAt: (value: string, index: number) => string,
    maxSlices = 6
): DonutSlice[] {
    const slices = items.map((item, index) => ({
        label: item.value || '(empty)',
        value: item.requests,
        color: colorAt(item.value, index),
    }));
    if (slices.length <= maxSlices + 1) return slices;
    const rest = slices.slice(maxSlices);
    return [
        ...slices.slice(0, maxSlices),
        {
            label: 'Other',
            value: rest.reduce((sum, slice) => sum + slice.value, 0),
            color: '#94a3b8',
        },
    ];
}

export default function NodeDetail() {
    const navigate = useNavigate();
    const { nodeId = '', '*': detailPath = '' } = useParams();
    const { clusterId } = useCluster();
    const api = useMemo(() => nodesApi(clusterId), [clusterId]);
    const analytics = useMemo(() => analyticsApi(clusterId), [clusterId]);
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
    const pathParts = detailPath.split('/').filter(Boolean);
    const requestedTab = pathParts[0] || 'overview';
    const tab: DetailTab = tabs.some((item) => item.id === requestedTab)
        ? (requestedTab as DetailTab)
        : 'overview';
    const requestedSettingsPage = pathParts[1] || 'network';
    const settingsPage: SettingsPage = requestedSettingsPage === 'cache' ? 'cache' : 'network';
    const canonicalDetailPath = tab === 'settings' ? `settings/${settingsPage}` : tab;
    const previousDetailPathRef = useRef(detailPath);
    const [node, setNode] = useState<Node | null>(null);
    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [groups, setGroups] = useState<ClusterGroup[]>([]);
    const [regions, setRegions] = useState<ClusterRegion[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [monitoringPeriod, setMonitoringPeriod] = useState<'24h' | '30d'>('24h');
    const [snapshot, setSnapshot] = useState<NodeSnapshot | null>(null);
    const [runtime, setRuntime] = useState<NodeRuntimePoint[]>([]);
    const [traffic, setTraffic] = useState<TrafficPoint[]>([]);
    const [monitoringLoading, setMonitoringLoading] = useState(false);
    const [domains, setDomains] = useState<DistributionItem[]>([]);
    const [extensions, setExtensions] = useState<DistributionItem[]>([]);
    const [statuses, setStatuses] = useState<DistributionItem[]>([]);
    const [methods, setMethods] = useState<DistributionItem[]>([]);

    const [dnsLineIds, setDnsLineIds] = useState<Set<string>>(new Set());
    const [dnsSaving, setDnsSaving] = useState(false);
    const [newAddress, setNewAddress] = useState('');
    const [addressAdding, setAddressAdding] = useState(false);
    const [editingAddressId, setEditingAddressId] = useState('');
    const [editingAddress, setEditingAddress] = useState('');
    const [addressBusyId, setAddressBusyId] = useState('');
    const [cache, setCache] = useState<NodeCacheConfig | null>(null);
    const [maxSizeGB, setMaxSizeGB] = useState('0');
    const [cacheSaving, setCacheSaving] = useState(false);
    const [cacheMessage, setCacheMessage] = useState('');

    const [sshIp, setSshIp] = useState('');
    const [sshPort, setSshPort] = useState('22');
    const [sshUser, setSshUser] = useState('root');
    const [authMethod, setAuthMethod] = useState<SSHAuthMethod>('password');
    const [password, setPassword] = useState('');
    const [privateKey, setPrivateKey] = useState('');
    const [passphrase, setPassphrase] = useState('');
    const [testing, setTesting] = useState(false);
    const [reinstalling, setReinstalling] = useState(false);
    const [testedFingerprint, setTestedFingerprint] = useState('');
    const [testAttemptFingerprint, setTestAttemptFingerprint] = useState('');
    const [testMessage, setTestMessage] = useState('');
    const [testError, setTestError] = useState('');
    const [installation, setInstallation] = useState<NodeInstallationInfo | null>(null);
    const [installationLoading, setInstallationLoading] = useState(false);
    const [manualInitializing, setManualInitializing] = useState(false);
    const [manualInitializationMessage, setManualInitializationMessage] = useState('');
    const [manualInitializationError, setManualInitializationError] = useState('');
    const [logs, setLogs] = useState<NodeRequestLog[]>([]);
    const [logsLoading, setLogsLoading] = useState(false);
    const [logsError, setLogsError] = useState('');

    const applyNode = useCallback((value: Node) => {
        setNode(value);
        setDnsLineIds(new Set((value.dnsLines || []).map((line) => line.dnsLineId)));
        setCache(value.cacheConfig ?? null);
        setMaxSizeGB(bytesToGB(value.cacheConfig?.max_size_bytes ?? 0));
        setSshIp((current) => current || value.addresses[0]?.address || '');
    }, []);

    const load = useCallback(async () => {
        if (!clusterId || !nodeId) return;
        setLoading(true);
        try {
            const [nodeData, lineData, groupData, regionData] = await Promise.all([
                api.get(nodeId),
                cluster.dnsLines(),
                cluster.groups(),
                cluster.regions(),
            ]);
            applyNode(nodeData);
            setDnsLines(lineData);
            setGroups(groupData);
            setRegions(regionData);
            setError('');
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load node');
        } finally {
            setLoading(false);
        }
    }, [api, applyNode, cluster, clusterId, nodeId]);

    const refreshNode = useCallback(async () => {
        if (!nodeId) return;
        const value = await api.get(nodeId);
        applyNode(value);
    }, [api, applyNode, nodeId]);

    useEffect(() => {
        void load();
    }, [load]);

    useEffect(() => {
        if (tab !== 'installation' || !nodeId) return;
        setInstallationLoading(true);
        setInstallation(null);
        setManualInitializationMessage('');
        setManualInitializationError('');
        api.installation(nodeId)
            .then((value) => {
                setInstallation(value);
                setError('');
            })
            .catch((loadError) =>
                setError(
                    loadError instanceof ApiError
                        ? loadError.message
                        : 'Failed to load installation information'
                )
            )
            .finally(() => setInstallationLoading(false));
    }, [api, nodeId, tab]);

    const loadLogs = useCallback(async () => {
        if (!nodeId) return;
        setLogsLoading(true);
        setLogs([]);
        setLogsError('');
        try {
            setLogs(await analytics.nodeLogs(nodeId, 200));
        } catch (loadError) {
            setLogsError(
                loadError instanceof ApiError ? loadError.message : 'Failed to load runtime logs'
            );
        } finally {
            setLogsLoading(false);
        }
    }, [analytics, nodeId]);

    useEffect(() => {
        if (tab === 'logs') void loadLogs();
    }, [loadLogs, tab]);

    useEffect(() => {
        if (!nodeId) return;
        if (detailPath !== canonicalDetailPath) {
            navigate(`/nodes/${nodeId}/${canonicalDetailPath}`, { replace: true });
        }
    }, [canonicalDetailPath, detailPath, navigate, nodeId]);

    useLayoutEffect(() => {
        previousDetailPathRef.current = detailPath;
    }, [detailPath]);

    const loadMonitoring = useCallback(async () => {
        if (!clusterId || !nodeId) return;
        setMonitoringLoading(true);
        const distributionParams = {
            node_id: nodeId,
            period: '24h' as const,
            sort: 'requests' as const,
            limit: 10,
        };
        try {
            const [
                snapshotData,
                runtimeData,
                trafficData,
                domainData,
                extensionData,
                statusData,
                methodData,
            ] = await Promise.all([
                analytics.latestNodeRuntime(nodeId),
                analytics.nodeRuntime({ node_id: nodeId, period: '12h' }),
                analytics.traffic({ node_id: nodeId, period: monitoringPeriod }),
                analytics.rankings('domain', distributionParams),
                analytics.distributions('extension', distributionParams),
                analytics.distributions('status', distributionParams),
                analytics.distributions('method', distributionParams),
            ]);
            setSnapshot(snapshotData[0] ?? null);
            setRuntime(runtimeData.series);
            setTraffic(trafficData.series);
            setDomains(domainData);
            setExtensions(extensionData);
            setStatuses(statusData);
            setMethods(methodData);
        } catch (monitoringError) {
            setError(
                monitoringError instanceof ApiError
                    ? monitoringError.message
                    : 'Failed to load node monitoring'
            );
        } finally {
            setMonitoringLoading(false);
        }
    }, [analytics, clusterId, monitoringPeriod, nodeId]);

    useEffect(() => {
        if (tab !== 'overview') return;
        void loadMonitoring();
        const refresh = window.setInterval(() => void loadMonitoring(), 60_000);
        return () => window.clearInterval(refresh);
    }, [loadMonitoring, tab]);

    const dnsOptions = useMemo(
        () =>
            dnsLines.map((line) => ({
                id: line.id,
                name: line.name,
                detail: line.providerCode,
            })),
        [dnsLines]
    );
    const groupNames = useMemo(
        () =>
            (node?.groupMemberships || []).map(
                (membership) =>
                    groups.find((group) => group.id === membership.groupId)?.name ||
                    membership.groupId
            ),
        [groups, node?.groupMemberships]
    );
    const regionNames = useMemo(
        () =>
            (node?.regionMemberships || []).map(
                (membership) =>
                    regions.find((region) => region.id === membership.regionId)?.name ||
                    membership.regionId
            ),
        [node?.regionMemberships, regions]
    );

    const saveDNSLines = async () => {
        if (!node) return;
        setDnsSaving(true);
        setError('');
        try {
            await api.updateDNSLines(node.id, Array.from(dnsLineIds));
            await refreshNode();
        } catch (saveError) {
            setError(
                saveError instanceof ApiError ? saveError.message : 'Failed to update DNS lines'
            );
        } finally {
            setDnsSaving(false);
        }
    };

    const addAddress = async () => {
        if (!node || !newAddress.trim()) return;
        setAddressAdding(true);
        setError('');
        try {
            await api.addAddress(node.id, {
                address: newAddress.trim(),
            });
            setNewAddress('');
            await refreshNode();
        } catch (addressError) {
            setError(
                addressError instanceof ApiError ? addressError.message : 'Failed to add address'
            );
        } finally {
            setAddressAdding(false);
        }
    };

    const saveAddress = async (addressId: string) => {
        if (!node || !editingAddress.trim()) return;
        setAddressBusyId(addressId);
        setError('');
        try {
            await api.updateAddress(node.id, addressId, { address: editingAddress.trim() });
            setEditingAddressId('');
            setEditingAddress('');
            await refreshNode();
        } catch (addressError) {
            setError(
                addressError instanceof ApiError ? addressError.message : 'Failed to update address'
            );
        } finally {
            setAddressBusyId('');
        }
    };

    const removeAddress = async (addressId: string, address: string) => {
        if (!node || !window.confirm(`Delete IP address ${address}?`)) return;
        setAddressBusyId(addressId);
        setError('');
        try {
            await api.deleteAddress(node.id, addressId);
            await refreshNode();
        } catch (addressError) {
            setError(
                addressError instanceof ApiError ? addressError.message : 'Failed to delete address'
            );
        } finally {
            setAddressBusyId('');
        }
    };

    const saveCache = async () => {
        if (!node || !cache) return;
        setCacheSaving(true);
        setCacheMessage('');
        setError('');
        try {
            const result = await api.updateCacheConfig(node.id, {
                ...cache,
                auto_max_size: false,
            });
            setCache(result.cache_config);
            setCacheMessage(
                result.synced
                    ? 'Configuration saved and synchronized to the node.'
                    : result.sync_error
                      ? `Saved, but synchronization failed: ${result.sync_error}`
                      : 'Configuration saved. The node is currently unavailable for synchronization.'
            );
        } catch (cacheError) {
            setError(
                cacheError instanceof ApiError ? cacheError.message : 'Failed to update cache'
            );
        } finally {
            setCacheSaving(false);
        }
    };

    const ssh = useMemo<NodeSSH>(
        () => ({
            entry_ip: sshIp.trim(),
            port: Number(sshPort) || 22,
            user: sshUser.trim(),
            password: authMethod === 'password' ? password || undefined : undefined,
            private_key: authMethod === 'private_key' ? privateKey || undefined : undefined,
            passphrase: authMethod === 'private_key' ? passphrase || undefined : undefined,
        }),
        [authMethod, passphrase, password, privateKey, sshIp, sshPort, sshUser]
    );
    const fingerprint = useMemo(() => JSON.stringify(ssh), [ssh]);
    const fingerprintRef = useRef(fingerprint);
    fingerprintRef.current = fingerprint;
    const connectionVerified = testedFingerprint === fingerprint;
    const canTestConnection = Boolean(
        sshIp.trim() && sshUser.trim() && (authMethod === 'password' ? password : privateKey)
    );

    const testConnection = async () => {
        const tested = fingerprint;
        setTesting(true);
        setTestedFingerprint('');
        setTestAttemptFingerprint(tested);
        setTestMessage('');
        setTestError('');
        try {
            const result = await api.testConnection(ssh);
            if (fingerprintRef.current !== tested) return;
            setTestedFingerprint(tested);
            setTestMessage(`Connected successfully · ${result.architecture}`);
        } catch (testConnectionError) {
            setTestError(
                testConnectionError instanceof ApiError
                    ? testConnectionError.message
                    : 'Failed to test SSH connection'
            );
        } finally {
            setTesting(false);
        }
    };

    const reinstall = async () => {
        if (!node || !connectionVerified) return;
        if (!confirm(`Reinstall the agent on "${node.name}"?`)) return;
        setReinstalling(true);
        setError('');
        try {
            await api.reinstall(
                node.id,
                ssh,
                node.status === 'PENDING' || node.status === 'INSTALLING'
            );
            setTestedFingerprint('');
            setPassword('');
            setPrivateKey('');
            setPassphrase('');
            navigate(`/nodes/${node.id}/overview`);
            await refreshNode();
        } catch (reinstallError) {
            setError(
                reinstallError instanceof ApiError
                    ? reinstallError.message
                    : 'Failed to reinstall node'
            );
        } finally {
            setReinstalling(false);
        }
    };

    const filledTraffic = useMemo(
        () => fillTrafficSeries(traffic, monitoringPeriod),
        [traffic, monitoringPeriod]
    );
    const trafficChart = useMemo(
        () =>
            filledTraffic.map((point) => ({
                bucket: point.bucket,
                values: {
                    traffic: point.ingress_bytes + point.egress_bytes,
                    cache: point.cache_egress_bytes,
                },
            })),
        [filledTraffic]
    );
    const requestChart = useMemo(
        () =>
            filledTraffic.map((point) => ({
                bucket: point.bucket,
                values: { requests: point.requests },
            })),
        [filledTraffic]
    );
    const cpuMemoryChart = useMemo(
        () =>
            runtime.map((point) => ({
                bucket: point.bucket,
                values: {
                    cpu: point.cpu_usage_percent,
                    memory:
                        point.memory_total_bytes > 0
                            ? (point.memory_used_bytes / point.memory_total_bytes) * 100
                            : 0,
                },
            })),
        [runtime]
    );
    const loadChart = useMemo(
        () =>
            runtime.map((point) => ({
                bucket: point.bucket,
                values: { load1: point.load_1, load5: point.load_5, load15: point.load_15 },
            })),
        [runtime]
    );
    const cacheChart = useMemo(
        () =>
            runtime.map((point) => ({
                bucket: point.bucket,
                values: { used: point.cache_used_bytes },
            })),
        [runtime]
    );
    const extensionSlices = useMemo(
        () => toSlices(extensions, (_value, index) => slicePalette[index % slicePalette.length]),
        [extensions]
    );
    const hostnameSlices = useMemo(
        () => toSlices(domains, (_value, index) => slicePalette[(index + 3) % slicePalette.length]),
        [domains]
    );
    const statusSlices = useMemo(() => toSlices(statuses, statusColor), [statuses]);
    const methodSlices = useMemo(
        () =>
            toSlices(
                methods,
                (value, index) =>
                    methodColors[value.toUpperCase()] ?? slicePalette[index % slicePalette.length]
            ),
        [methods]
    );

    if (!clusterId) return <FormError message='Select a cluster to view this node.' />;

    const addressSummary = node?.addresses.length
        ? node.addresses.map((item) => item.address).join(', ')
        : '—';
    const cacheIsValid = Boolean(
        cache?.cache_directory.trim().startsWith('/') &&
            cache.max_disk_usage_percent >= 1 &&
            cache.max_disk_usage_percent <= 95
    );
    const enteringNetworkTab =
        previousDetailPathRef.current !== detailPath &&
        (tab === 'overview' || tab === 'logs' || tab === 'installation');
    const tabContentLoading =
        enteringNetworkTab ||
        (tab === 'overview'
            ? monitoringLoading
            : tab === 'logs'
              ? logsLoading
              : tab === 'installation'
                ? installationLoading
                : tab === 'settings' && settingsPage === 'network'
                  ? dnsSaving || addressAdding || addressBusyId !== ''
                  : tab === 'settings' && settingsPage === 'cache'
                    ? cacheSaving
                    : false);
    const navigateTo = (nextTab: DetailTab, subpage?: SettingsPage) => {
        const nextPath = `${nextTab}${subpage ? `/${subpage}` : ''}`;
        if (detailPath === nextPath) return;
        navigate(`/nodes/${nodeId}/${nextPath}`);
    };
    const initializeManualInstallation = async () => {
        if (!node) return;
        setManualInitializing(true);
        setManualInitializationMessage('');
        setManualInitializationError('');
        try {
            const result = await api.initializeInstallation(node.id);
            await refreshNode();
            setInstallation((current) =>
                current ? { ...current, status: result.status, install_error: undefined } : current
            );
            setManualInitializationMessage(
                result.message || 'The agent is initialized and ready for health monitoring.'
            );
        } catch (initializeError) {
            setManualInitializationError(
                initializeError instanceof ApiError
                    ? initializeError.message
                    : 'Failed to initialize the manually installed agent'
            );
        } finally {
            setManualInitializing(false);
        }
    };

    return (
        <div className='space-y-6'>
            <nav aria-label='Breadcrumb' className='flex flex-wrap items-center gap-2 text-sm'>
                <button
                    className='text-muted transition-colors hover:text-foreground'
                    type='button'
                    onClick={() => navigate('/nodes')}
                >
                    Nodes
                </button>
                <span className='text-muted'>/</span>
                <button
                    className='text-muted transition-colors hover:text-foreground'
                    type='button'
                    onClick={() => navigateTo('overview')}
                >
                    {node?.name || nodeId}
                </button>
                <span className='text-muted'>/</span>
                <span className='font-medium'>{tabs.find((item) => item.id === tab)?.label}</span>
                {tab === 'settings' && (
                    <>
                        <span className='text-muted'>/</span>
                        <span className='font-medium'>
                            {settingsPage === 'network' ? 'Network & DNS' : 'Cache'}
                        </span>
                    </>
                )}
            </nav>
            <PageHeader
                actions={
                    <Button variant='ghost' onPress={() => navigate('/nodes')}>
                        <ArrowLeft className='mr-1.5 h-4 w-4' />
                        Back to nodes
                    </Button>
                }
                subtitle='Configuration, routing, storage, and installation management.'
                title={node?.name || 'Node details'}
            />

            {error && <FormError message={error} />}
            {loading && (
                <ContentCard className='p-10 text-center text-sm text-muted'>
                    Loading node…
                </ContentCard>
            )}

            {!loading && node && (
                <>
                    {node.installError && (
                        <ContentCard className='overflow-hidden p-0' noPadding>
                            <div className='border-t border-danger/20 bg-danger/10 px-5 py-3 text-sm text-danger'>
                                <div className='font-medium'>Installation error</div>
                                <pre className='mt-1 whitespace-pre-wrap break-words font-sans text-xs leading-5'>
                                    {node.installError}
                                </pre>
                            </div>
                        </ContentCard>
                    )}

                    <div className='flex w-fit items-center gap-1 overflow-x-auto rounded-xl bg-surface p-1'>
                        {tabs.map((item) => {
                            const Icon = item.icon;
                            return (
                                <button
                                    className={`flex shrink-0 items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors ${
                                        tab === item.id
                                            ? 'bg-surface-secondary text-foreground shadow-sm'
                                            : 'text-muted hover:text-foreground'
                                    }`}
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
                                <div className='flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
                                    <div>
                                        <h2 className='text-sm font-semibold'>Monitoring</h2>
                                        <p className='mt-0.5 text-xs text-muted'>
                                            Live health and traffic. Runtime data refreshes every
                                            minute.
                                        </p>
                                    </div>
                                    <div className='flex items-center gap-2'>
                                        {(['24h', '30d'] as const).map((period) => (
                                            <Button
                                                key={period}
                                                size='sm'
                                                variant={
                                                    monitoringPeriod === period
                                                        ? 'primary'
                                                        : 'secondary'
                                                }
                                                onPress={() => {
                                                    if (period === monitoringPeriod) return;
                                                    setMonitoringLoading(true);
                                                    setMonitoringPeriod(period);
                                                }}
                                            >
                                                {period}
                                            </Button>
                                        ))}
                                        <Button
                                            isDisabled={monitoringLoading}
                                            size='sm'
                                            variant='secondary'
                                            onPress={() => void loadMonitoring()}
                                        >
                                            <RefreshCw className='mr-1.5 h-3.5 w-3.5' />
                                            Refresh
                                        </Button>
                                    </div>
                                </div>

                                <div className='flex flex-wrap items-center gap-x-6 gap-y-2 rounded-xl bg-surface px-4 py-2.5 text-sm'>
                                    <span className='flex items-center gap-2'>
                                        <span className='text-xs text-muted'>IPs</span>
                                        <span className='font-mono text-xs'>{addressSummary}</span>
                                    </span>
                                    <span className='flex items-center gap-2'>
                                        <span className='text-xs text-muted'>DNS lines</span>
                                        <span>{dnsLineIds.size || 'Default'}</span>
                                    </span>
                                    <span className='flex items-center gap-2'>
                                        <span className='text-xs text-muted'>Site configs</span>
                                        <span>{node.siteConfigVersions?.length || 0}</span>
                                    </span>
                                </div>

                                {!snapshot && node.hardwareProfile && (
                                    <div className='mb-3 grid gap-3 sm:grid-cols-2'>
                                        <StatCell
                                            footer={node.hardwareProfile.architecture}
                                            label='CPU model'
                                            value={node.hardwareProfile.cpu_model}
                                        />
                                        <StatCell
                                            footer={`Measured ${new Date(node.hardwareProfile.measured_at).toLocaleString()} · ${formatBytes(node.hardwareProfile.benchmark_bytes ?? 0)} sample`}
                                            label='Cache disk write speed'
                                            value={
                                                node.hardwareProfile
                                                    .cache_disk_write_bytes_per_second
                                                    ? formatRate(
                                                          node.hardwareProfile
                                                              .cache_disk_write_bytes_per_second
                                                      )
                                                    : 'Unavailable'
                                            }
                                        />
                                    </div>
                                )}

                                {snapshot ? (
                                    <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4 mb-3'>
                                        <StatCell
                                            footer={`Last report ${new Date(snapshot.bucket).toLocaleString()}`}
                                            label='Node status'
                                            value={<StatusBadge status={node.status} />}
                                        />
                                        <StatCell
                                            footer={
                                                node.hardwareProfile
                                                    ? `${node.hardwareProfile.cpu_model} · ${node.hardwareProfile.architecture}`
                                                    : 'Recorded during agent installation'
                                            }
                                            label='CPU'
                                            value={`${snapshot.cpu_usage_percent.toFixed(1)}%`}
                                        />
                                        <StatCell
                                            footer={`${formatBytes(snapshot.memory_used_bytes)} / ${formatBytes(snapshot.memory_total_bytes)}`}
                                            label='Memory'
                                            value={
                                                snapshot.memory_total_bytes > 0
                                                    ? `${((snapshot.memory_used_bytes / snapshot.memory_total_bytes) * 100).toFixed(1)}%`
                                                    : '—'
                                            }
                                        />
                                        <StatCell
                                            footer={`${snapshot.connections.toLocaleString()} established connections`}
                                            label='Requests/min'
                                            value={snapshot.requests_per_minute.toLocaleString()}
                                        />
                                        <StatCell
                                            footer='Request bytes received plus response bytes sent'
                                            label='Traffic bandwidth'
                                            value={formatRate(
                                                snapshot.ingress_bytes_per_second +
                                                    snapshot.egress_bytes_per_second
                                            )}
                                        />
                                        <StatCell
                                            footer='Response bytes served directly from edge cache'
                                            label='Cache traffic'
                                            value={formatRate(
                                                snapshot.cache_egress_bytes_per_second
                                            )}
                                        />
                                        <StatCell
                                            footer={snapshot.cache_directory}
                                            label='Cache usage'
                                            value={formatBytes(snapshot.cache_used_bytes)}
                                        />
                                        <StatCell
                                            footer={
                                                node.hardwareProfile
                                                    ? `Measured ${new Date(node.hardwareProfile.measured_at).toLocaleString()} · ${formatBytes(node.hardwareProfile.benchmark_bytes ?? 0)} sample`
                                                    : 'Run during agent installation'
                                            }
                                            label='Cache disk write speed'
                                            value={
                                                node.hardwareProfile
                                                    ?.cache_disk_write_bytes_per_second
                                                    ? formatRate(
                                                          node.hardwareProfile
                                                              .cache_disk_write_bytes_per_second
                                                      )
                                                    : 'Unavailable'
                                            }
                                        />
                                    </div>
                                ) : (
                                    <ContentCard>
                                        <div className='py-8 text-center text-sm text-muted'>
                                            {monitoringLoading
                                                ? 'Loading node monitoring…'
                                                : 'No runtime reports have been received from this node.'}
                                        </div>
                                    </ContentCard>
                                )}

                                <div className='grid gap-3 md:grid-cols-2 xl:grid-cols-4'>
                                    <ContentCard
                                        allowOverflow
                                        className='h-full'
                                        title={`Traffic · ${monitoringPeriod}`}
                                    >
                                        <TimeSeriesChart
                                            ariaLabel={`${node.name} traffic trend`}
                                            data={trafficChart}
                                            height={220}
                                            series={[
                                                {
                                                    key: 'traffic',
                                                    label: 'Traffic',
                                                    color: '#3b82f6',
                                                },
                                                {
                                                    key: 'cache',
                                                    label: 'Cache traffic',
                                                    color: '#10b981',
                                                },
                                            ]}
                                            valueFormatter={formatBytes}
                                        />
                                    </ContentCard>
                                    <ContentCard
                                        allowOverflow
                                        className='h-full'
                                        title={`Requests · ${monitoringPeriod}`}
                                    >
                                        <TimeSeriesChart
                                            ariaLabel={`${node.name} requests trend`}
                                            data={requestChart}
                                            height={220}
                                            series={[
                                                {
                                                    key: 'requests',
                                                    label: 'Requests',
                                                    color: '#8b5cf6',
                                                },
                                            ]}
                                        />
                                    </ContentCard>
                                    <ContentCard
                                        allowOverflow
                                        className='h-full'
                                        title='CPU & memory · 12h'
                                    >
                                        <TimeSeriesChart
                                            ariaLabel={`${node.name} CPU and memory trend`}
                                            data={cpuMemoryChart}
                                            height={220}
                                            series={[
                                                { key: 'cpu', label: 'CPU', color: '#f59e0b' },
                                                {
                                                    key: 'memory',
                                                    label: 'Memory',
                                                    color: '#3b82f6',
                                                },
                                            ]}
                                            valueFormatter={(value) => `${value.toFixed(1)}%`}
                                        />
                                    </ContentCard>
                                    <ContentCard
                                        allowOverflow
                                        className='h-full'
                                        title='Load average · 12h'
                                    >
                                        <TimeSeriesChart
                                            ariaLabel={`${node.name} load average trend`}
                                            data={loadChart}
                                            height={220}
                                            series={[
                                                { key: 'load1', label: '1m', color: '#ef4444' },
                                                { key: 'load5', label: '5m', color: '#f59e0b' },
                                                { key: 'load15', label: '15m', color: '#10b981' },
                                            ]}
                                        />
                                    </ContentCard>
                                    <ContentCard
                                        allowOverflow
                                        className='h-full'
                                        title='Cache usage · 12h'
                                    >
                                        <TimeSeriesChart
                                            ariaLabel={`${node.name} cache usage trend`}
                                            data={cacheChart}
                                            height={220}
                                            series={[
                                                { key: 'used', label: 'Used', color: '#8b5cf6' },
                                            ]}
                                            valueFormatter={formatBytes}
                                        />
                                    </ContentCard>
                                </div>

                                <div className='grid gap-3 md:grid-cols-2 xl:grid-cols-4'>
                                    <ContentCard className='h-full' title='Top domains · 24h'>
                                        <RankingBars items={domains} limit={5} />
                                    </ContentCard>
                                    <ContentCard className='h-full' title='File types · 24h'>
                                        <DonutChart
                                            compact
                                            ariaLabel='File type distribution'
                                            slices={extensionSlices}
                                        />
                                    </ContentCard>
                                    <ContentCard className='h-full' title='Hostnames · 24h'>
                                        <DonutChart
                                            compact
                                            ariaLabel='Hostname distribution'
                                            slices={hostnameSlices}
                                        />
                                    </ContentCard>
                                    <ContentCard className='h-full' title='Status codes · 24h'>
                                        <DonutChart
                                            compact
                                            ariaLabel='Status code distribution'
                                            slices={statusSlices}
                                        />
                                    </ContentCard>
                                    <ContentCard className='h-full' title='Methods · 24h'>
                                        <DonutChart
                                            compact
                                            ariaLabel='Request method distribution'
                                            slices={methodSlices}
                                        />
                                    </ContentCard>
                                </div>
                            </div>
                        )}

                        {tab === 'details' && (
                            <div className='grid gap-4 lg:grid-cols-2'>
                                <ContentCard title='Node information'>
                                    <div className='grid gap-5 sm:grid-cols-2'>
                                        <FormField label='Node ID'>
                                            <span className='break-all font-mono text-sm'>
                                                {node.id}
                                            </span>
                                        </FormField>
                                        <FormField label='Status'>
                                            <StatusBadge status={node.status} />
                                        </FormField>
                                        <FormField label='Agent version'>
                                            <span className='text-sm'>
                                                {node.version || 'Not reported'}
                                            </span>
                                        </FormField>
                                        <FormField label='Last heartbeat'>
                                            <span className='text-sm'>
                                                {node.heartbeatAt
                                                    ? new Date(node.heartbeatAt).toLocaleString()
                                                    : 'Never'}
                                            </span>
                                        </FormField>
                                        <FormField label='Created'>
                                            <span className='text-sm'>
                                                {new Date(node.createdAt).toLocaleString()}
                                            </span>
                                        </FormField>
                                        <FormField label='Updated'>
                                            <span className='text-sm'>
                                                {new Date(node.updatedAt).toLocaleString()}
                                            </span>
                                        </FormField>
                                        <FormField label='Architecture'>
                                            <span className='text-sm'>
                                                {node.hardwareProfile?.architecture || 'Unknown'}
                                            </span>
                                        </FormField>
                                        <FormField label='CPU model'>
                                            <span className='text-sm'>
                                                {node.hardwareProfile?.cpu_model || 'Unknown'}
                                            </span>
                                        </FormField>
                                    </div>
                                </ContentCard>
                                <ContentCard title='Membership'>
                                    <div className='space-y-5'>
                                        <FormField label='Groups'>
                                            <NameChips
                                                empty='No groups assigned'
                                                names={groupNames}
                                            />
                                        </FormField>
                                        <FormField label='Regions'>
                                            <NameChips
                                                empty='No regions assigned'
                                                names={regionNames}
                                            />
                                        </FormField>
                                    </div>
                                </ContentCard>
                                <ContentCard className='lg:col-span-2' title='Site configurations'>
                                    <div className='divide-y divide-border'>
                                        {(node.siteConfigVersions || []).map((version) => (
                                            <div
                                                className='flex items-center justify-between gap-3 py-3'
                                                key={version.site_id}
                                            >
                                                <span className='truncate font-mono text-sm'>
                                                    {version.site_id}
                                                </span>
                                                <div className='flex items-center gap-2'>
                                                    <span className='text-sm text-muted'>
                                                        v{version.version}
                                                    </span>
                                                    <StatusBadge status={version.status} />
                                                </div>
                                            </div>
                                        ))}
                                        {(node.siteConfigVersions || []).length === 0 && (
                                            <p className='py-5 text-sm text-muted'>
                                                No site configurations published.
                                            </p>
                                        )}
                                    </div>
                                </ContentCard>
                            </div>
                        )}

                        {tab === 'logs' && (
                            <ContentCard className='overflow-hidden p-0' noPadding>
                                <div className='flex items-center justify-between border-b border-border px-5 py-4'>
                                    <div>
                                        <h2 className='text-sm font-semibold'>
                                            Recent runtime logs
                                        </h2>
                                        <p className='mt-0.5 text-xs text-muted'>
                                            Latest requests handled by this node.
                                        </p>
                                    </div>
                                    <Button
                                        isDisabled={logsLoading}
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => void loadLogs()}
                                    >
                                        <RefreshCw className='mr-1.5 h-4 w-4' />
                                        Refresh
                                    </Button>
                                </div>
                                {logsError && (
                                    <div className='p-5'>
                                        <FormError message={logsError} />
                                    </div>
                                )}
                                <div className='overflow-x-auto'>
                                    <table className='w-full min-w-[900px] text-left text-sm'>
                                        <thead className='bg-surface-secondary/50 text-xs uppercase text-muted'>
                                            <tr>
                                                <th className='px-4 py-3'>Time</th>
                                                <th className='px-4 py-3'>Request</th>
                                                <th className='px-4 py-3'>Status</th>
                                                <th className='px-4 py-3'>Cache</th>
                                                <th className='px-4 py-3'>Upstream</th>
                                                <th className='px-4 py-3'>Duration</th>
                                            </tr>
                                        </thead>
                                        <tbody className='divide-y divide-border'>
                                            {logs.map((entry) => (
                                                <tr key={`${entry.event_time}-${entry.request_id}`}>
                                                    <td className='whitespace-nowrap px-4 py-3 text-muted'>
                                                        {new Date(
                                                            entry.event_time
                                                        ).toLocaleString()}
                                                    </td>
                                                    <td className='max-w-md px-4 py-3'>
                                                        <div className='font-medium'>
                                                            {entry.method} {entry.hostname}
                                                        </div>
                                                        <div className='truncate font-mono text-xs text-muted'>
                                                            {entry.path}
                                                        </div>
                                                    </td>
                                                    <td className='px-4 py-3'>
                                                        {entry.status_code}
                                                    </td>
                                                    <td className='px-4 py-3'>
                                                        {entry.cache_status || '—'}
                                                    </td>
                                                    <td className='px-4 py-3 font-mono text-xs'>
                                                        {entry.upstream_address || '—'}
                                                    </td>
                                                    <td className='px-4 py-3'>
                                                        {(entry.duration_us / 1000).toFixed(1)} ms
                                                    </td>
                                                </tr>
                                            ))}
                                            {!logsLoading && logs.length === 0 && !logsError && (
                                                <tr>
                                                    <td
                                                        className='px-4 py-10 text-center text-muted'
                                                        colSpan={6}
                                                    >
                                                        No runtime logs.
                                                    </td>
                                                </tr>
                                            )}
                                        </tbody>
                                    </table>
                                </div>
                            </ContentCard>
                        )}

                        {tab === 'settings' && settingsPage === 'network' && (
                            <div className='grid gap-6 lg:grid-cols-[200px_1fr]'>
                                <SettingsNavigation
                                    page={settingsPage}
                                    onChange={(page) => navigateTo('settings', page)}
                                />
                                <div className='grid gap-4'>
                                    <ContentCard className='overflow-visible p-0' noPadding>
                                        <SectionTitle
                                            description='Choose every provider routing line served by this node.'
                                            icon={Globe2}
                                            title='DNS routing lines'
                                        />
                                        <div className='space-y-4 p-5'>
                                            <SearchableMultiAddField
                                                addLabel='Modify DNS lines'
                                                dialogTitle='Configure DNS lines'
                                                emptyLabel='Default routing only'
                                                itemLabel='DNS line'
                                                options={dnsOptions}
                                                searchPlaceholder='Search by name or provider code…'
                                                selected={dnsLineIds}
                                                onChange={setDnsLineIds}
                                            />
                                            <div className='flex justify-end border-t border-border pt-4'>
                                                <Button
                                                    isDisabled={dnsSaving}
                                                    onPress={() => void saveDNSLines()}
                                                >
                                                    <Save className='mr-1.5 h-4 w-4' />
                                                    {dnsSaving
                                                        ? 'Saving…'
                                                        : 'Save DNS configuration'}
                                                </Button>
                                            </div>
                                        </div>
                                    </ContentCard>

                                    <ContentCard className='overflow-hidden p-0' noPadding>
                                        <SectionTitle
                                            description='IP addresses used for traffic and agent communication.'
                                            icon={Network}
                                            title='Network addresses'
                                        />
                                        <div className='divide-y divide-border'>
                                            {node.addresses.map((address) => (
                                                <div
                                                    className='flex items-center justify-between gap-3 px-5 py-3'
                                                    key={address.id}
                                                >
                                                    {editingAddressId === address.id ? (
                                                        <Input
                                                            autoFocus
                                                            aria-label='IP address'
                                                            className='max-w-md flex-1 font-mono'
                                                            value={editingAddress}
                                                            variant='secondary'
                                                            onChange={(event) =>
                                                                setEditingAddress(
                                                                    event.target.value
                                                                )
                                                            }
                                                        />
                                                    ) : (
                                                        <span className='font-mono text-sm'>
                                                            {address.address}
                                                        </span>
                                                    )}
                                                    <div className='flex shrink-0 items-center gap-1'>
                                                        {editingAddressId === address.id ? (
                                                            <>
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label='Save IP address'
                                                                    isDisabled={
                                                                        !editingAddress.trim() ||
                                                                        addressBusyId === address.id
                                                                    }
                                                                    size='sm'
                                                                    onPress={() =>
                                                                        void saveAddress(address.id)
                                                                    }
                                                                >
                                                                    <Check className='h-4 w-4' />
                                                                </Button>
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label='Cancel editing'
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() =>
                                                                        setEditingAddressId('')
                                                                    }
                                                                >
                                                                    <X className='h-4 w-4' />
                                                                </Button>
                                                            </>
                                                        ) : (
                                                            <>
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label='Edit IP address'
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() => {
                                                                        setEditingAddressId(
                                                                            address.id
                                                                        );
                                                                        setEditingAddress(
                                                                            address.address
                                                                        );
                                                                    }}
                                                                >
                                                                    <Pencil className='h-4 w-4' />
                                                                </Button>
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label='Delete IP address'
                                                                    isDisabled={
                                                                        addressBusyId === address.id
                                                                    }
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() =>
                                                                        void removeAddress(
                                                                            address.id,
                                                                            address.address
                                                                        )
                                                                    }
                                                                >
                                                                    <Trash2 className='h-4 w-4 text-danger' />
                                                                </Button>
                                                            </>
                                                        )}
                                                    </div>
                                                </div>
                                            ))}
                                            {node.addresses.length === 0 && (
                                                <div className='px-5 py-6 text-sm text-muted'>
                                                    No IP addresses. This node is excluded from DNS
                                                    resolution.
                                                </div>
                                            )}
                                        </div>
                                        <div className='space-y-3 border-t border-border bg-surface-secondary/20 p-5'>
                                            <div className='text-sm font-medium'>Add address</div>
                                            <div className='flex flex-col gap-3 sm:flex-row sm:items-center'>
                                                <Input
                                                    aria-label='New IP address'
                                                    className='flex-1'
                                                    placeholder='203.0.113.10'
                                                    value={newAddress}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        setNewAddress(event.target.value)
                                                    }
                                                />
                                                <Button
                                                    isDisabled={!newAddress.trim() || addressAdding}
                                                    onPress={() => void addAddress()}
                                                >
                                                    <Plus className='mr-1.5 h-4 w-4' />
                                                    {addressAdding ? 'Adding…' : 'Add'}
                                                </Button>
                                            </div>
                                        </div>
                                    </ContentCard>
                                </div>
                            </div>
                        )}

                        {tab === 'settings' && settingsPage === 'cache' && cache && (
                            <div className='grid gap-6 lg:grid-cols-[200px_1fr]'>
                                <SettingsNavigation
                                    page={settingsPage}
                                    onChange={(page) => navigateTo('settings', page)}
                                />
                                <ContentCard className='max-w-4xl overflow-hidden p-0' noPadding>
                                    <SectionTitle
                                        description='Control the on-disk response cache and synchronize it to the edge agent.'
                                        icon={HardDrive}
                                        title='Disk cache configuration'
                                    />
                                    <div className='space-y-6 p-5'>
                                        <FormField
                                            error={
                                                cache.cache_directory.trim().startsWith('/')
                                                    ? undefined
                                                    : 'Use an absolute path beginning with /.'
                                            }
                                            hint='Absolute directory on the node used to store cached responses.'
                                            htmlFor='node-cache-directory'
                                            label='Cache directory'
                                        >
                                            <Input
                                                id='node-cache-directory'
                                                value={cache.cache_directory}
                                                variant='secondary'
                                                onChange={(event) =>
                                                    setCache({
                                                        ...cache,
                                                        cache_directory: event.target.value,
                                                    })
                                                }
                                            />
                                        </FormField>
                                        <div className='grid gap-5 sm:grid-cols-2'>
                                            <FormField
                                                hint='Enter 0 for no cache size limit.'
                                                htmlFor='node-cache-max-size'
                                                label='Maximum size (GB)'
                                            >
                                                <Input
                                                    id='node-cache-max-size'
                                                    min={0}
                                                    step={0.1}
                                                    type='number'
                                                    value={maxSizeGB}
                                                    variant='secondary'
                                                    onChange={(event) => {
                                                        setMaxSizeGB(event.target.value);
                                                        setCache({
                                                            ...cache,
                                                            auto_max_size: false,
                                                            max_size_bytes: Math.max(
                                                                0,
                                                                Math.round(
                                                                    Number(
                                                                        event.target.value || 0
                                                                    ) * bytesPerGB
                                                                )
                                                            ),
                                                        });
                                                    }}
                                                />
                                            </FormField>
                                            <FormField
                                                error={
                                                    cache.max_disk_usage_percent < 1 ||
                                                    cache.max_disk_usage_percent > 95
                                                        ? 'Enter a percentage from 1 to 95.'
                                                        : undefined
                                                }
                                                hint='The agent stops growing the cache before this threshold.'
                                                htmlFor='node-cache-disk-percent'
                                                label='Maximum disk usage (%)'
                                            >
                                                <Input
                                                    id='node-cache-disk-percent'
                                                    max={95}
                                                    min={1}
                                                    type='number'
                                                    value={String(cache.max_disk_usage_percent)}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        setCache({
                                                            ...cache,
                                                            max_disk_usage_percent: Number(
                                                                event.target.value
                                                            ),
                                                        })
                                                    }
                                                />
                                            </FormField>
                                        </div>
                                        {cacheMessage && (
                                            <div className='rounded-xl border border-border bg-surface-secondary/40 px-4 py-3 text-sm'>
                                                {cacheMessage}
                                            </div>
                                        )}
                                        <div className='flex justify-end border-t border-border pt-4'>
                                            <Button
                                                isDisabled={cacheSaving || !cacheIsValid}
                                                onPress={() => void saveCache()}
                                            >
                                                <Save className='mr-1.5 h-4 w-4' />
                                                {cacheSaving ? 'Saving…' : 'Save and synchronize'}
                                            </Button>
                                        </div>
                                    </div>
                                </ContentCard>
                            </div>
                        )}

                        {tab === 'installation' && (
                            <div className='mx-auto max-w-6xl space-y-5'>
                                {installation && (
                                    <div className='flex flex-col gap-4 rounded-xl border border-border bg-surface px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                                        <div className='min-w-0'>
                                            <div className='flex flex-wrap items-center gap-3'>
                                                <h2 className='text-base font-semibold'>
                                                    Agent installation
                                                </h2>
                                                <StatusBadge status={installation.status} />
                                            </div>
                                            <p className='mt-1 text-sm text-muted'>
                                                Node ID{' '}
                                                <span className='break-all font-mono text-xs text-foreground'>
                                                    {installation.node_id}
                                                </span>
                                            </p>
                                        </div>
                                    </div>
                                )}

                                {installation?.install_error && (
                                    <FormError message={installation.install_error} />
                                )}

                                <ContentCard className='overflow-visible p-0' noPadding>
                                    <div className='flex flex-col gap-3 border-b border-border bg-surface-secondary/30 px-5 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-6'>
                                        <div className='flex items-start gap-3'>
                                            <span className='flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-primary text-primary-foreground'>
                                                <RefreshCw className='h-4 w-4' />
                                            </span>
                                            <div>
                                                <div className='text-sm font-semibold'>
                                                    Install automatically with SSH
                                                </div>
                                                <div className='mt-0.5 text-xs leading-5 text-muted'>
                                                    Test access, upload the agent, configure
                                                    systemd, and start the service.
                                                </div>
                                            </div>
                                        </div>
                                        <span className='self-start rounded-full border border-border bg-surface px-2.5 py-1 text-xs font-medium text-muted sm:self-auto'>
                                            Recommended
                                        </span>
                                    </div>

                                    <div className='px-5 py-2 sm:px-6'>
                                        <FormRow
                                            hint='The address must be reachable from the control plane.'
                                            htmlFor='reinstall-ssh-ip'
                                            label='SSH host'
                                            required
                                        >
                                            <Input
                                                className='w-full'
                                                id='reinstall-ssh-ip'
                                                required
                                                value={sshIp}
                                                variant='secondary'
                                                onChange={(event) => setSshIp(event.target.value)}
                                            />
                                        </FormRow>
                                        <FormRow
                                            htmlFor='reinstall-ssh-port'
                                            label='SSH port'
                                            required
                                        >
                                            <Input
                                                className='w-full'
                                                id='reinstall-ssh-port'
                                                required
                                                type='number'
                                                value={sshPort}
                                                variant='secondary'
                                                onChange={(event) => setSshPort(event.target.value)}
                                            />
                                        </FormRow>
                                        <FormRow
                                            htmlFor='reinstall-ssh-user'
                                            label='SSH user'
                                            required
                                        >
                                            <Input
                                                className='w-full'
                                                id='reinstall-ssh-user'
                                                required
                                                value={sshUser}
                                                variant='secondary'
                                                onChange={(event) => setSshUser(event.target.value)}
                                            />
                                        </FormRow>
                                        <FormRow label='Authentication method' required>
                                            <div className='grid gap-3 sm:grid-cols-2'>
                                                <button
                                                    aria-pressed={authMethod === 'password'}
                                                    className={`flex cursor-pointer items-start gap-3 rounded-xl border p-4 text-left transition-colors ${
                                                        authMethod === 'password'
                                                            ? 'border-accent bg-accent/10'
                                                            : 'border-border bg-surface hover:bg-surface-secondary'
                                                    }`}
                                                    type='button'
                                                    onClick={() => {
                                                        setAuthMethod('password');
                                                        setPrivateKey('');
                                                        setPassphrase('');
                                                    }}
                                                >
                                                    <LockKeyhole className='mt-0.5 h-5 w-5 shrink-0 text-muted' />
                                                    <span>
                                                        <span className='block text-sm font-semibold'>
                                                            Password
                                                        </span>
                                                        <span className='mt-1 block text-xs leading-5 text-muted'>
                                                            Authenticate with the SSH account
                                                            password.
                                                        </span>
                                                    </span>
                                                </button>
                                                <button
                                                    aria-pressed={authMethod === 'private_key'}
                                                    className={`flex cursor-pointer items-start gap-3 rounded-xl border p-4 text-left transition-colors ${
                                                        authMethod === 'private_key'
                                                            ? 'border-accent bg-accent/10'
                                                            : 'border-border bg-surface hover:bg-surface-secondary'
                                                    }`}
                                                    type='button'
                                                    onClick={() => {
                                                        setAuthMethod('private_key');
                                                        setPassword('');
                                                    }}
                                                >
                                                    <KeyRound className='mt-0.5 h-5 w-5 shrink-0 text-muted' />
                                                    <span>
                                                        <span className='block text-sm font-semibold'>
                                                            Private key
                                                        </span>
                                                        <span className='mt-1 block text-xs leading-5 text-muted'>
                                                            Authenticate with a PEM-encoded private
                                                            key.
                                                        </span>
                                                    </span>
                                                </button>
                                            </div>
                                        </FormRow>

                                        {authMethod === 'password' ? (
                                            <FormRow
                                                htmlFor='reinstall-ssh-password'
                                                label='Password'
                                                required
                                            >
                                                <Input
                                                    className='w-full'
                                                    id='reinstall-ssh-password'
                                                    required
                                                    type='password'
                                                    value={password}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        setPassword(event.target.value)
                                                    }
                                                />
                                            </FormRow>
                                        ) : (
                                            <>
                                                <FormRow
                                                    hint='Paste the complete PEM key, including the BEGIN and END lines.'
                                                    htmlFor='reinstall-ssh-key'
                                                    label='Private key PEM'
                                                    required
                                                >
                                                    <TextArea
                                                        className='w-full font-mono text-xs'
                                                        id='reinstall-ssh-key'
                                                        required
                                                        rows={8}
                                                        spellCheck={false}
                                                        value={privateKey}
                                                        variant='secondary'
                                                        onChange={(event) =>
                                                            setPrivateKey(event.target.value)
                                                        }
                                                    />
                                                </FormRow>
                                                <FormRow
                                                    htmlFor='reinstall-ssh-passphrase'
                                                    label='Private key passphrase'
                                                >
                                                    <Input
                                                        className='w-full'
                                                        id='reinstall-ssh-passphrase'
                                                        type='password'
                                                        value={passphrase}
                                                        variant='secondary'
                                                        onChange={(event) =>
                                                            setPassphrase(event.target.value)
                                                        }
                                                    />
                                                </FormRow>
                                            </>
                                        )}

                                        <div className='border-t border-border py-4'>
                                            <div className='flex flex-wrap items-center gap-3'>
                                                <Button
                                                    isDisabled={!canTestConnection || testing}
                                                    type='button'
                                                    variant='secondary'
                                                    onPress={() => void testConnection()}
                                                >
                                                    {testing
                                                        ? 'Testing connection…'
                                                        : 'Test SSH connection'}
                                                </Button>
                                                {connectionVerified && (
                                                    <span className='inline-flex items-center gap-1.5 text-sm text-success'>
                                                        <Check className='h-4 w-4' />
                                                        {testMessage}
                                                    </span>
                                                )}
                                            </div>
                                            {testAttemptFingerprint === fingerprint &&
                                                testError && (
                                                    <div className='mt-3'>
                                                        <FormError message={testError} />
                                                    </div>
                                                )}
                                            {testedFingerprint && !connectionVerified && (
                                                <p className='mt-2 text-sm text-warning'>
                                                    SSH settings changed. Test the connection again
                                                    before reinstalling the node.
                                                </p>
                                            )}
                                        </div>

                                        <div className='flex flex-col-reverse gap-3 border-t border-border py-5 sm:flex-row sm:items-center sm:justify-between'>
                                            <p className='text-xs leading-5 text-muted'>
                                                Reinstallation replaces the agent binary and
                                                restarts the service. Node identity is preserved.
                                            </p>
                                            <Button
                                                className='shrink-0'
                                                isDisabled={!connectionVerified || reinstalling}
                                                type='button'
                                                onPress={() => void reinstall()}
                                            >
                                                {reinstalling
                                                    ? 'Starting installation…'
                                                    : 'Install with SSH'}
                                            </Button>
                                        </div>
                                    </div>
                                </ContentCard>

                                {installation && (
                                    <details className='group overflow-hidden rounded-xl border border-border bg-surface'>
                                        <summary className='flex cursor-pointer list-none items-center justify-between gap-4 px-5 py-4 transition-colors hover:bg-surface-secondary/50 sm:px-6'>
                                            <div className='flex items-start gap-3'>
                                                <span className='flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-surface-secondary text-muted'>
                                                    <Terminal className='h-4 w-4' />
                                                </span>
                                                <div>
                                                    <div className='text-sm font-semibold'>
                                                        Manual installation
                                                    </div>
                                                    <div className='mt-0.5 text-xs leading-5 text-muted'>
                                                        Download the agent files when SSH automation
                                                        is unavailable.
                                                    </div>
                                                </div>
                                            </div>
                                            <span className='text-xs font-medium text-muted group-open:hidden'>
                                                Show instructions
                                            </span>
                                            <span className='hidden text-xs font-medium text-muted group-open:inline'>
                                                Hide instructions
                                            </span>
                                        </summary>

                                        <div className='space-y-6 border-t border-border px-5 py-5 sm:px-6'>
                                            <div>
                                                <h3 className='text-sm font-semibold'>
                                                    Download files
                                                </h3>
                                                <p className='mt-1 text-xs leading-5 text-muted'>
                                                    Choose one binary, then download the identity
                                                    and service files for this node.
                                                </p>
                                                <div className='mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
                                                    {installation.architectures.map(
                                                        (architecture) => (
                                                            <a
                                                                className='button button--secondary justify-center w-full'
                                                                download
                                                                href={api.installationArtifactUrl(
                                                                    node.id,
                                                                    `binary/${architecture}`
                                                                )}
                                                                key={architecture}
                                                            >
                                                                <Download className='h-4 w-4' />
                                                                Linux {architecture}
                                                            </a>
                                                        )
                                                    )}
                                                    <a
                                                        className='button button--secondary justify-center w-full'
                                                        download
                                                        href={api.installationArtifactUrl(
                                                            node.id,
                                                            'identity'
                                                        )}
                                                    >
                                                        <Download className='h-4 w-4' /> Identity
                                                    </a>
                                                    <a
                                                        className='button button--secondary justify-center w-full'
                                                        download
                                                        href={api.installationArtifactUrl(
                                                            node.id,
                                                            'service'
                                                        )}
                                                    >
                                                        <Download className='h-4 w-4' /> systemd
                                                        unit
                                                    </a>
                                                </div>
                                            </div>

                                            <div className='grid gap-5 lg:grid-cols-[minmax(0,1.35fr)_minmax(280px,0.65fr)]'>
                                                <div>
                                                    <h3 className='text-sm font-semibold'>
                                                        Install and start the service
                                                    </h3>
                                                    <ol className='mt-2 list-decimal space-y-2 pl-5 text-sm leading-6 text-muted'>
                                                        <li>Copy all three files to the node.</li>
                                                        <li>
                                                            Run the commands below as root or with
                                                            sudo.
                                                        </li>
                                                        <li>
                                                            Confirm that systemd reports the service
                                                            as active.
                                                        </li>
                                                    </ol>
                                                    <pre className='mt-4 overflow-x-auto rounded-xl bg-surface-secondary p-4 text-xs leading-6'>
                                                        {`install -d -m 0700 /opt/goveto-edge/agent
install -m 0600 identity.json /opt/goveto-edge/agent/identity.json
install -m 0755 goveto-edge-agent-linux-ARCH /usr/local/bin/goveto-edge-agent
install -m 0644 goveto-edge-agent.service /etc/systemd/system/goveto-edge-agent.service
systemctl daemon-reload
systemctl enable goveto-edge-agent
systemctl restart goveto-edge-agent
systemctl status goveto-edge-agent`}
                                                    </pre>
                                                </div>

                                                <div className='rounded-xl border border-border bg-surface-secondary/35 p-4 flex flex-col'>
                                                    <h3 className='text-sm font-semibold'>
                                                        Finish initialization
                                                    </h3>
                                                    <p className='mt-2 text-sm leading-6 text-muted'>
                                                        After the service is active, verify the
                                                        Agent, synchronize node configuration, and
                                                        start health monitoring.
                                                    </p>
                                                    <div className='flex-1'></div>
                                                    <Button
                                                        className='mt-4 w-full justify-center'
                                                        isDisabled={
                                                            manualInitializing ||
                                                            installation.status === 'INSTALLING' ||
                                                            installation.status === 'DISABLED'
                                                        }
                                                        variant='secondary'
                                                        onPress={() =>
                                                            void initializeManualInstallation()
                                                        }
                                                    >
                                                        <RefreshCw className='mr-1.5 h-4 w-4' />
                                                        {manualInitializing
                                                            ? 'Initializing…'
                                                            : 'Verify and initialize'}
                                                    </Button>
                                                    {manualInitializationMessage && (
                                                        <p className='mt-3 text-sm leading-6 text-success'>
                                                            {manualInitializationMessage}
                                                        </p>
                                                    )}
                                                    {manualInitializationError && (
                                                        <div className='mt-3'>
                                                            <FormError
                                                                message={manualInitializationError}
                                                            />
                                                        </div>
                                                    )}
                                                    {installation.status === 'INSTALLING' && (
                                                        <p className='mt-3 text-xs leading-5 text-warning'>
                                                            Wait for the current SSH installation to
                                                            finish before initializing manually.
                                                        </p>
                                                    )}
                                                    {installation.status === 'DISABLED' && (
                                                        <p className='mt-3 text-xs leading-5 text-warning'>
                                                            Enable this node before initializing it.
                                                        </p>
                                                    )}
                                                </div>
                                            </div>

                                            <details className='rounded-xl border border-border px-4 py-3'>
                                                <summary className='cursor-pointer text-sm font-medium'>
                                                    View generated configuration
                                                </summary>
                                                <div className='mt-4 grid gap-4 lg:grid-cols-2'>
                                                    <FormField label='identity.json'>
                                                        <pre className='max-h-64 overflow-auto rounded-xl bg-surface-secondary p-4 text-xs leading-5'>
                                                            {installation.identity_json}
                                                        </pre>
                                                    </FormField>
                                                    <FormField label='goveto-edge-agent.service'>
                                                        <pre className='max-h-64 overflow-auto rounded-xl bg-surface-secondary p-4 text-xs leading-5'>
                                                            {installation.service_unit}
                                                        </pre>
                                                    </FormField>
                                                </div>
                                            </details>
                                        </div>
                                    </details>
                                )}
                            </div>
                        )}
                    </LoadingSurface>
                </>
            )}
        </div>
    );
}
