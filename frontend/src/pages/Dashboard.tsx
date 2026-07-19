import type {
    DistributionItem,
    MonitoringOverview,
    Node,
    NodeRuntimePoint,
    SiteSummary,
    TrafficPoint,
} from '@/api';
import type { DonutSlice } from '@/components/DonutChart.tsx';

import { Button } from '@heroui/react';
import { RefreshCw, Server } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, analyticsApi, nodesApi, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DonutChart } from '@/components/DonutChart.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { RankingBars } from '@/components/RankingBars.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { TimeSeriesChart } from '@/components/TimeSeriesChart.tsx';
import { useCluster } from '@/hooks/useCluster.ts';
import { fillTrafficSeries } from '@/utils/timeseries.ts';

type Period = '24h' | '30d';
const slicePalette = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#06b6d4', '#ec4899'];

function toSlices(items: DistributionItem[], offset = 0): DonutSlice[] {
    return items.slice(0, 6).map((item, index) => ({
        label: item.value || '(empty)',
        value: item.requests,
        color: slicePalette[(index + offset) % slicePalette.length],
    }));
}

function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const unit = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatBandwidth(bytesPerSecond: number) {
    const bits = bytesPerSecond * 8;
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
    if (!Number.isFinite(bits) || bits <= 0) return '0 bps';
    const unit = Math.min(Math.floor(Math.log(bits) / Math.log(1000)), units.length - 1);
    return `${(bits / 1000 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function totalTraffic(item?: { ingress_bytes: number; egress_bytes: number }) {
    return (item?.ingress_bytes ?? 0) + (item?.egress_bytes ?? 0);
}

function aggregateRuntime(points: NodeRuntimePoint[]) {
    const buckets = new Map<
        string,
        {
            count: number;
            cpu: number;
            memoryUsed: number;
            memoryTotal: number;
            load1: number;
            load5: number;
            load15: number;
            cacheUsed: number;
        }
    >();
    for (const point of points) {
        const current = buckets.get(point.bucket) ?? {
            count: 0,
            cpu: 0,
            memoryUsed: 0,
            memoryTotal: 0,
            load1: 0,
            load5: 0,
            load15: 0,
            cacheUsed: 0,
        };
        current.count += 1;
        current.cpu += point.cpu_usage_percent;
        current.memoryUsed += point.memory_used_bytes;
        current.memoryTotal += point.memory_total_bytes;
        current.load1 += point.load_1;
        current.load5 += point.load_5;
        current.load15 += point.load_15;
        current.cacheUsed += point.cache_used_bytes;
        buckets.set(point.bucket, current);
    }
    return Array.from(buckets, ([bucket, value]) => ({
        bucket,
        cpu: value.count > 0 ? value.cpu / value.count : 0,
        memory: value.memoryTotal > 0 ? (value.memoryUsed / value.memoryTotal) * 100 : 0,
        load1: value.count > 0 ? value.load1 / value.count : 0,
        load5: value.count > 0 ? value.load5 / value.count : 0,
        load15: value.count > 0 ? value.load15 / value.count : 0,
        cacheUsed: value.cacheUsed,
    })).sort((left, right) => new Date(left.bucket).getTime() - new Date(right.bucket).getTime());
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

export default function Dashboard() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const analytics = useMemo(() => analyticsApi(clusterId), [clusterId]);
    const nodeApi = useMemo(() => nodesApi(clusterId), [clusterId]);
    const siteApi = useMemo(() => sitesApi(clusterId), [clusterId]);
    const [period, setPeriod] = useState<Period>('24h');
    const [nodes, setNodes] = useState<Node[]>([]);
    const [sites, setSites] = useState<SiteSummary[]>([]);
    const [runtime, setRuntime] = useState<NodeRuntimePoint[]>([]);
    const [overview, setOverview] = useState<MonitoringOverview | null>(null);
    const [traffic, setTraffic] = useState<TrafficPoint[]>([]);
    const [domains, setDomains] = useState<DistributionItem[]>([]);
    const [extensions, setExtensions] = useState<DistributionItem[]>([]);
    const [hostnames, setHostnames] = useState<DistributionItem[]>([]);
    const [statuses, setStatuses] = useState<DistributionItem[]>([]);
    const [methods, setMethods] = useState<DistributionItem[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const load = useCallback(async () => {
        if (!clusterId) return;
        setLoading(true);
        try {
            const [
                nodeData,
                siteData,
                runtimeData,
                overviewData,
                trafficData,
                domainData,
                extensionData,
                hostnameData,
                statusData,
                methodData,
            ] = await Promise.all([
                nodeApi.list(),
                siteApi.list(),
                analytics.nodeRuntime({ period: '12h' }),
                analytics.overview(),
                analytics.traffic({ period }),
                analytics.rankings('domain', {
                    period: '24h',
                    sort: 'requests',
                    limit: 10,
                }),
                analytics.distributions('extension', {
                    period: '24h',
                    sort: 'requests',
                    limit: 10,
                }),
                analytics.distributions('hostname', {
                    period: '24h',
                    sort: 'requests',
                    limit: 10,
                }),
                analytics.distributions('status', {
                    period: '24h',
                    sort: 'requests',
                    limit: 10,
                }),
                analytics.distributions('method', {
                    period: '24h',
                    sort: 'requests',
                    limit: 10,
                }),
            ]);
            setNodes(nodeData);
            setSites(siteData);
            setRuntime(runtimeData.series);
            setOverview(overviewData);
            setTraffic(trafficData.series);
            setDomains(domainData);
            setExtensions(extensionData);
            setHostnames(hostnameData);
            setStatuses(statusData);
            setMethods(methodData);
            setError('');
        } catch (loadError) {
            setError(
                loadError instanceof ApiError
                    ? loadError.message
                    : 'Failed to load cluster overview'
            );
        } finally {
            setLoading(false);
        }
    }, [analytics, clusterId, nodeApi, period, siteApi]);

    useEffect(() => {
        void load();
        const refresh = window.setInterval(() => void load(), 60_000);
        return () => window.clearInterval(refresh);
    }, [load]);

    const onlineNodes = nodes.filter((node) => node.status === 'ONLINE').length;
    const chartTraffic = fillTrafficSeries(traffic, period);
    const bucketSeconds = period === '24h' ? 3600 : 86400;
    const bandwidthChart = chartTraffic.map((point) => ({
        bucket: point.bucket,
        values: { bandwidth: totalTraffic(point) / bucketSeconds },
    }));
    const trafficChart = chartTraffic.map((point) => ({
        bucket: point.bucket,
        values: {
            traffic: totalTraffic(point),
            cache: point.cache_egress_bytes,
        },
    }));
    const requestChart = chartTraffic.map((point) => ({
        bucket: point.bucket,
        values: { requests: point.requests },
    }));
    const aggregatedRuntime = aggregateRuntime(runtime);
    const cpuMemoryChart = aggregatedRuntime.map((point) => ({
        bucket: point.bucket,
        values: { cpu: point.cpu, memory: point.memory },
    }));
    const loadChart = aggregatedRuntime.map((point) => ({
        bucket: point.bucket,
        values: { load1: point.load1, load5: point.load5, load15: point.load15 },
    }));
    const cacheChart = aggregatedRuntime.map((point) => ({
        bucket: point.bucket,
        values: { used: point.cacheUsed },
    }));
    const extensionSlices = toSlices(extensions, 0);
    const hostnameSlices = toSlices(hostnames, 1);
    const statusSlices = toSlices(statuses, 2);
    const methodSlices = toSlices(methods, 3);

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='Cluster traffic and resource status.' title='Overview' />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to view its overview.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            {error && (
                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                    {error}
                </div>
            )}

            <div className='flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
                <PageHeader subtitle='Cluster traffic and resource status.' title='Overview' />
                <div className='flex items-center gap-2'>
                    {(['24h', '30d'] as Period[]).map((value) => (
                        <Button
                            key={value}
                            size='sm'
                            variant={period === value ? 'primary' : 'secondary'}
                            onPress={() => setPeriod(value)}
                        >
                            {value}
                        </Button>
                    ))}
                    <Button
                        isDisabled={loading}
                        size='sm'
                        variant='secondary'
                        onPress={() => void load()}
                    >
                        <RefreshCw className='mr-1.5 h-3.5 w-3.5' />
                        Refresh
                    </Button>
                </div>
            </div>

            <div className='flex flex-wrap items-center gap-x-6 gap-y-2 rounded-xl bg-surface px-4 py-2.5 text-sm'>
                <span className='flex items-center gap-2'>
                    <span className='text-xs text-muted'>Nodes</span>
                    <span>{nodes.length}</span>
                </span>
                <span className='flex items-center gap-2'>
                    <span className='text-xs text-muted'>Online</span>
                    <span className={onlineNodes > 0 ? 'text-success' : 'text-danger'}>
                        {onlineNodes}
                    </span>
                </span>
                <span className='flex items-center gap-2'>
                    <span className='text-xs text-muted'>Sites</span>
                    <span>{sites.length}</span>
                </span>
                <span className='flex items-center gap-2'>
                    <span className='text-xs text-muted'>Domains</span>
                    <span>{sites.reduce((sum, site) => sum + (site.domains?.length ?? 0), 0)}</span>
                </span>
            </div>

            <ContentCard noPadding>
                <div className='flex flex-col gap-3 border-b border-border px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                    <div>
                        <h2 className='text-sm font-semibold'>Cluster nodes</h2>
                        <p className='mt-1 text-xs text-muted'>
                            {onlineNodes} online, {nodes.length - onlineNodes} offline or
                            unavailable
                        </p>
                    </div>
                    <Button size='sm' variant='ghost' onPress={() => navigate('/nodes')}>
                        Manage nodes
                    </Button>
                </div>
                {nodes.length === 0 ? (
                    <div className='px-5 py-10 text-center text-sm text-muted'>
                        No nodes registered in this cluster.
                    </div>
                ) : (
                    <div className='grid gap-px bg-border sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4'>
                        {nodes.map((node) => (
                            <button
                                className='flex min-w-0 items-center gap-3 bg-surface px-4 py-3 text-left transition-colors hover:bg-surface-secondary/50'
                                key={node.id}
                                type='button'
                                onClick={() => navigate(`/nodes/${node.id}/overview`)}
                            >
                                <span
                                    className={`flex h-9 w-9 shrink-0 items-center justify-center rounded-lg ${
                                        node.status === 'ONLINE'
                                            ? 'bg-success/10 text-success'
                                            : 'bg-surface-secondary text-muted'
                                    }`}
                                >
                                    <Server className='h-4 w-4' />
                                </span>
                                <span className='min-w-0 flex-1'>
                                    <span className='block truncate text-sm font-medium'>
                                        {node.name}
                                    </span>
                                    <span className='mt-0.5 block truncate text-xs text-muted'>
                                        {node.heartbeatAt
                                            ? `Last seen ${new Date(node.heartbeatAt).toLocaleString()}`
                                            : 'No heartbeat reported'}
                                    </span>
                                </span>
                                <StatusBadge status={node.status} />
                            </button>
                        ))}
                    </div>
                )}
            </ContentCard>

            <div className='mb-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
                <StatCell
                    footer={`${onlineNodes} of ${nodes.length} nodes online`}
                    label='Cluster status'
                    tone={
                        nodes.length > 0 && onlineNodes === nodes.length
                            ? 'success'
                            : onlineNodes === 0
                              ? 'danger'
                              : 'default'
                    }
                    value={
                        nodes.length === 0
                            ? 'No nodes'
                            : onlineNodes === nodes.length
                              ? 'Healthy'
                              : onlineNodes === 0
                                ? 'Offline'
                                : 'Degraded'
                    }
                />
                <StatCell
                    footer={`${sites.reduce((sum, site) => sum + (site.domains?.length ?? 0), 0)} domains`}
                    label='Sites'
                    value={sites.length.toLocaleString()}
                />
                <StatCell
                    footer='Latest hourly average'
                    label='Current bandwidth'
                    value={formatBandwidth(overview?.current_bandwidth_bps ?? 0)}
                />
                <StatCell
                    footer='Distinct clients since midnight UTC'
                    label='Today unique IPs'
                    value={(overview?.today_unique_ips ?? 0).toLocaleString()}
                />
                <StatCell
                    footer={`${(overview?.today.requests ?? 0).toLocaleString()} requests`}
                    label='Today traffic'
                    value={formatBytes(totalTraffic(overview?.today))}
                />
                <StatCell
                    footer={`${(overview?.yesterday.requests ?? 0).toLocaleString()} requests`}
                    label='Yesterday traffic'
                    value={formatBytes(totalTraffic(overview?.yesterday))}
                />
                <StatCell
                    footer={`${(overview?.month.requests ?? 0).toLocaleString()} requests`}
                    label='Month traffic'
                    value={formatBytes(totalTraffic(overview?.month))}
                />
                <StatCell
                    footer='Highest hourly average this month'
                    label='Month peak bandwidth'
                    value={formatBandwidth(overview?.month_peak_bandwidth_bps ?? 0)}
                />
            </div>

            <div className='grid gap-3 md:grid-cols-2 xl:grid-cols-4'>
                <ContentCard
                    allowOverflow
                    className='flex h-full min-h-[310px] flex-col'
                    contentClassName='flex min-h-0 flex-1 flex-col text-sm'
                    title={`Bandwidth · ${period}`}
                >
                    <TimeSeriesChart
                        ariaLabel={`${period} cluster bandwidth`}
                        data={bandwidthChart}
                        height={220}
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
                <ContentCard
                    allowOverflow
                    className='flex h-full min-h-[310px] flex-col'
                    contentClassName='flex min-h-0 flex-1 flex-col text-sm'
                    title={`Traffic · ${period}`}
                >
                    <TimeSeriesChart
                        ariaLabel={`${period} cluster traffic`}
                        data={trafficChart}
                        height={220}
                        series={[
                            { key: 'traffic', label: 'Traffic', color: '#2563eb' },
                            { key: 'cache', label: 'Cache traffic', color: '#059669' },
                        ]}
                        valueFormatter={formatBytes}
                    />
                </ContentCard>
                <ContentCard
                    allowOverflow
                    className='flex h-full min-h-[310px] flex-col'
                    contentClassName='flex min-h-0 flex-1 flex-col text-sm'
                    title={`Requests · ${period}`}
                >
                    <TimeSeriesChart
                        ariaLabel={`${period} cluster requests`}
                        data={requestChart}
                        height={220}
                        series={[{ key: 'requests', label: 'Requests', color: '#2563eb' }]}
                    />
                </ContentCard>
                <ContentCard
                    allowOverflow
                    className='flex h-full min-h-[310px] flex-col'
                    contentClassName='flex min-h-0 flex-1 flex-col text-sm'
                    title='CPU & memory · 12h'
                >
                    <TimeSeriesChart
                        ariaLabel='Cluster CPU and memory trend'
                        data={cpuMemoryChart}
                        height={220}
                        includeZero={false}
                        series={[
                            { key: 'cpu', label: 'CPU', color: '#f59e0b' },
                            { key: 'memory', label: 'Memory', color: '#3b82f6' },
                        ]}
                        valueFormatter={(value) => `${value.toFixed(1)}%`}
                    />
                </ContentCard>
                <ContentCard
                    allowOverflow
                    className='flex h-full min-h-[310px] flex-col'
                    contentClassName='flex min-h-0 flex-1 flex-col text-sm'
                    title='Load average · 12h'
                >
                    <TimeSeriesChart
                        ariaLabel='Cluster load average trend'
                        data={loadChart}
                        height={220}
                        includeZero={false}
                        series={[
                            { key: 'load1', label: '1m', color: '#ef4444' },
                            { key: 'load5', label: '5m', color: '#f59e0b' },
                            { key: 'load15', label: '15m', color: '#10b981' },
                        ]}
                    />
                </ContentCard>
                <ContentCard
                    allowOverflow
                    className='flex h-full min-h-[310px] flex-col'
                    contentClassName='flex min-h-0 flex-1 flex-col text-sm'
                    title='Cache usage · 12h'
                >
                    <TimeSeriesChart
                        ariaLabel='Cluster cache usage trend'
                        data={cacheChart}
                        height={220}
                        includeZero={false}
                        series={[{ key: 'used', label: 'Used', color: '#8b5cf6' }]}
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
                    <DonutChart compact ariaLabel='Hostname distribution' slices={hostnameSlices} />
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
    );
}
