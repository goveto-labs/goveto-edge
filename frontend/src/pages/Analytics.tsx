import type {
    DistributionItem,
    MonitoringOverview,
    Node,
    NodeRuntimePoint,
    TrafficPoint,
} from '@/api';

import { Button, Input, Label } from '@heroui/react';
import { Activity, CalendarDays, HardDrive, MousePointerClick, RefreshCw } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';

import { ApiError, analyticsApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatCard } from '@/components/StatCard.tsx';
import { TimeSeriesChart } from '@/components/TimeSeriesChart.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';

type Period = '24h' | '30d';

function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const unit = Math.min(
        Math.max(0, Math.floor(Math.log(bytes) / Math.log(1024))),
        units.length - 1
    );
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function totalTraffic(item?: { ingress_bytes: number; egress_bytes: number }) {
    return (item?.ingress_bytes ?? 0) + (item?.egress_bytes ?? 0);
}

function trafficOf(item: DistributionItem) {
    return item.ingress_bytes + item.egress_bytes;
}

function RankingTable({
    title,
    valueLabel,
    items,
}: {
    title: string;
    valueLabel: string;
    items: DistributionItem[];
}) {
    return (
        <DataTable
            empty={items.length === 0}
            emptyDescription='Data will appear after access logs are collected.'
            emptyTitle='No analytics data'
            title={title}
        >
            <thead>
                <tr>
                    <th>{valueLabel}</th>
                    <th>Requests</th>
                    <th>Traffic</th>
                </tr>
            </thead>
            <tbody>
                {items.map((item) => (
                    <tr key={item.value}>
                        <td className='max-w-sm truncate font-mono text-xs' title={item.value}>
                            {item.value || '(empty)'}
                        </td>
                        <td className='text-sm'>{item.requests.toLocaleString()}</td>
                        <td className='text-sm text-muted'>{formatBytes(trafficOf(item))}</td>
                    </tr>
                ))}
            </tbody>
        </DataTable>
    );
}

function Breakdown({ title, items }: { title: string; items: DistributionItem[] }) {
    const max = Math.max(1, ...items.map((item) => item.requests));
    return (
        <ContentCard title={title}>
            {items.length === 0 ? (
                <div className='py-10 text-center text-sm text-muted'>No analytics data</div>
            ) : (
                <div className='space-y-3'>
                    {items.slice(0, 10).map((item) => (
                        <div className='space-y-1.5' key={item.value}>
                            <div className='flex items-center justify-between gap-4 text-xs'>
                                <span className='truncate font-mono' title={item.value}>
                                    {item.value || '(empty)'}
                                </span>
                                <span className='shrink-0 text-muted'>
                                    {item.requests.toLocaleString()} requests
                                </span>
                            </div>
                            <div className='h-2 overflow-hidden rounded-full bg-surface-secondary'>
                                <div
                                    className='h-full rounded-full bg-primary'
                                    style={{
                                        width: `${Math.max(2, (item.requests / max) * 100)}%`,
                                    }}
                                />
                            </div>
                        </div>
                    ))}
                </div>
            )}
        </ContentCard>
    );
}

export default function Analytics() {
    const { clusterId } = useCluster();
    const api = useMemo(() => analyticsApi(clusterId), [clusterId]);
    const nodeApi = useMemo(() => nodesApi(clusterId), [clusterId]);
    const [period, setPeriod] = useState<Period>('24h');
    const [siteId, setSiteId] = useState('');
    const [overview, setOverview] = useState<MonitoringOverview | null>(null);
    const [traffic, setTraffic] = useState<TrafficPoint[]>([]);
    const [nodes, setNodes] = useState<Node[]>([]);
    const [selectedNode, setSelectedNode] = useState('');
    const [runtime, setRuntime] = useState<NodeRuntimePoint[]>([]);
    const [domains, setDomains] = useState<DistributionItem[]>([]);
    const [extensions, setExtensions] = useState<DistributionItem[]>([]);
    const [statuses, setStatuses] = useState<DistributionItem[]>([]);
    const [methods, setMethods] = useState<DistributionItem[]>([]);
    const [paths, setPaths] = useState<DistributionItem[]>([]);
    const [ipsByRequests, setIpsByRequests] = useState<DistributionItem[]>([]);
    const [ipsByTraffic, setIpsByTraffic] = useState<DistributionItem[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const load = useCallback(async () => {
        if (!clusterId) return;
        setLoading(true);
        const params = { site_id: siteId, period, limit: 10 } as const;
        try {
            const [
                overviewData,
                trafficData,
                nodeData,
                domainData,
                extensionData,
                statusData,
                methodData,
                pathData,
                ipRequestData,
                ipTrafficData,
            ] = await Promise.all([
                api.overview({ site_id: siteId }),
                api.traffic({ site_id: siteId, period }),
                nodeApi.list(),
                api.rankings('domain', { ...params, sort: 'requests' }),
                api.distributions('extension', { ...params, sort: 'requests' }),
                api.distributions('status', { ...params, sort: 'requests' }),
                api.distributions('method', { ...params, sort: 'requests' }),
                api.rankings('path', { ...params, sort: 'requests' }),
                api.rankings('ip', { ...params, sort: 'requests' }),
                api.rankings('ip', { ...params, sort: 'traffic' }),
            ]);
            setOverview(overviewData);
            setTraffic(trafficData.series);
            setNodes(nodeData);
            setSelectedNode((current) =>
                nodeData.some((node) => node.id === current) ? current : nodeData[0]?.id || ''
            );
            setDomains(domainData);
            setExtensions(extensionData);
            setStatuses(statusData);
            setMethods(methodData);
            setPaths(pathData);
            setIpsByRequests(ipRequestData);
            setIpsByTraffic(ipTrafficData);
            setError('');
        } catch (loadError) {
            setError(
                loadError instanceof ApiError ? loadError.message : 'Failed to load monitoring data'
            );
        } finally {
            setLoading(false);
        }
    }, [api, clusterId, nodeApi, period, siteId]);

    useAutoRefresh(load, Boolean(clusterId));

    const loadRuntime = useCallback(async () => {
        if (!clusterId || !selectedNode) {
            setRuntime([]);
            return;
        }
        try {
            const result = await api.nodeRuntime({ node_id: selectedNode, period: '12h' });
            setRuntime(result.series);
        } catch (runtimeError) {
            setError(
                runtimeError instanceof ApiError
                    ? runtimeError.message
                    : 'Failed to load node trends'
            );
        }
    }, [api, clusterId, selectedNode]);

    useAutoRefresh(loadRuntime, Boolean(clusterId && selectedNode));

    const trafficData = useMemo(
        () =>
            traffic.map((point) => ({
                bucket: point.bucket,
                values: {
                    traffic: point.ingress_bytes + point.egress_bytes,
                    cache: point.cache_egress_bytes,
                },
            })),
        [traffic]
    );
    const requestData = useMemo(
        () =>
            traffic.map((point) => ({
                bucket: point.bucket,
                values: { requests: point.requests },
            })),
        [traffic]
    );
    const cpuMemoryData = useMemo(
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
    const loadData = useMemo(
        () =>
            runtime.map((point) => ({
                bucket: point.bucket,
                values: { load1: point.load_1, load5: point.load_5, load15: point.load_15 },
            })),
        [runtime]
    );
    const cacheData = useMemo(
        () =>
            runtime.map((point) => ({
                bucket: point.bucket,
                values: { used: point.cache_used_bytes },
            })),
        [runtime]
    );

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Traffic, node runtime and cache monitoring.'
                    title='Analytics'
                />
                <ContentCard className='p-8 text-center'>
                    <div className='text-sm text-muted'>Select a cluster to view monitoring.</div>
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader subtitle='Traffic, node runtime and cache monitoring.' title='Analytics'>
                <Button isDisabled={loading} onPress={() => void load()}>
                    <RefreshCw className='mr-2 h-4 w-4' />
                    {loading ? 'Refreshing…' : 'Refresh'}
                </Button>
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <ContentCard title='Monitoring filters'>
                <div className='flex flex-wrap items-end gap-4'>
                    <div className='flex flex-col gap-1'>
                        <Label htmlFor='analytics-site-id'>Site ID (optional)</Label>
                        <Input
                            className='w-72'
                            id='analytics-site-id'
                            value={siteId}
                            variant='secondary'
                            onChange={(event) => setSiteId(event.target.value)}
                        />
                    </div>
                    <div className='flex gap-2'>
                        {(['24h', '30d'] as Period[]).map((value) => (
                            <Button
                                key={value}
                                variant={period === value ? 'primary' : 'secondary'}
                                onPress={() => setPeriod(value)}
                            >
                                {value}
                            </Button>
                        ))}
                    </div>
                </div>
            </ContentCard>

            {overview && (
                <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4'>
                    <StatCard
                        color='primary'
                        footer={`${overview.today.requests.toLocaleString()} requests · ${formatBytes(overview.today.cache_egress_bytes)} cache`}
                        icon={Activity}
                        label='Today traffic'
                        value={formatBytes(totalTraffic(overview.today))}
                    />
                    <StatCard
                        footer={`${overview.yesterday.requests.toLocaleString()} requests · ${formatBytes(overview.yesterday.cache_egress_bytes)} cache`}
                        icon={CalendarDays}
                        label='Yesterday traffic'
                        value={formatBytes(totalTraffic(overview.yesterday))}
                    />
                    <StatCard
                        color='success'
                        footer={`${overview.month.requests.toLocaleString()} requests · ${formatBytes(overview.month.cache_egress_bytes)} cache`}
                        icon={CalendarDays}
                        label='Current month traffic'
                        value={formatBytes(totalTraffic(overview.month))}
                    />
                    <StatCard
                        color='warning'
                        footer={`${formatBytes(totalTraffic(overview.today))} traffic · ${formatBytes(overview.today.cache_egress_bytes)} cache`}
                        icon={MousePointerClick}
                        label='Today requests'
                        value={overview.today.requests.toLocaleString()}
                    />
                </div>
            )}

            <div className='grid grid-cols-1 gap-4 xl:grid-cols-2'>
                <ContentCard allowOverflow title={`${period} traffic trend`}>
                    <TimeSeriesChart
                        ariaLabel={`${period} total and cache traffic trend`}
                        data={trafficData}
                        series={[
                            { key: 'traffic', label: 'Traffic', color: '#3b82f6' },
                            { key: 'cache', label: 'Cache traffic', color: '#10b981' },
                        ]}
                        valueFormatter={formatBytes}
                    />
                </ContentCard>
                <ContentCard allowOverflow title={`${period} request trend`}>
                    <TimeSeriesChart
                        ariaLabel={`${period} request trend`}
                        data={requestData}
                        series={[{ key: 'requests', label: 'Requests', color: '#8b5cf6' }]}
                    />
                </ContentCard>
            </div>

            <ContentCard
                allowOverflow
                action={
                    <select
                        aria-label='Node trend selection'
                        className='rounded-lg border border-border bg-surface px-3 py-2 text-sm'
                        value={selectedNode}
                        onChange={(event) => setSelectedNode(event.target.value)}
                    >
                        {nodes.map((node) => (
                            <option key={node.id} value={node.id}>
                                {node.name}
                            </option>
                        ))}
                    </select>
                }
                title='Node runtime trends (12h)'
            >
                <div className='grid grid-cols-1 gap-6 xl:grid-cols-2'>
                    <div>
                        <h3 className='mb-3 text-sm font-medium'>CPU and memory</h3>
                        <TimeSeriesChart
                            ariaLabel='Node CPU and memory trend over 12 hours'
                            data={cpuMemoryData}
                            series={[
                                { key: 'cpu', label: 'CPU', color: '#f59e0b' },
                                { key: 'memory', label: 'Memory', color: '#3b82f6' },
                            ]}
                            valueFormatter={(value) => `${value.toFixed(1)}%`}
                        />
                    </div>
                    <div>
                        <h3 className='mb-3 text-sm font-medium'>Load average</h3>
                        <TimeSeriesChart
                            ariaLabel='Node load average trend over 12 hours'
                            data={loadData}
                            series={[
                                { key: 'load1', label: '1m', color: '#ef4444' },
                                { key: 'load5', label: '5m', color: '#f59e0b' },
                                { key: 'load15', label: '15m', color: '#10b981' },
                            ]}
                        />
                    </div>
                    <div className='xl:col-span-2'>
                        <div className='mb-3 flex items-center gap-2 text-sm font-medium'>
                            <HardDrive className='h-4 w-4' /> Cache directory usage
                        </div>
                        <TimeSeriesChart
                            ariaLabel='Cache directory usage trend over 12 hours'
                            data={cacheData}
                            series={[{ key: 'used', label: 'Used', color: '#8b5cf6' }]}
                            valueFormatter={formatBytes}
                        />
                    </div>
                </div>
            </ContentCard>

            <div className='grid grid-cols-1 gap-4 xl:grid-cols-2'>
                <RankingTable items={domains} title='Domain request volume' valueLabel='Domain' />
                <Breakdown items={extensions} title='File extension composition' />
                <RankingTable
                    items={statuses}
                    title='Status code distribution'
                    valueLabel='Status'
                />
                <RankingTable
                    items={methods}
                    title='Request method distribution'
                    valueLabel='Method'
                />
            </div>

            <RankingTable items={paths} title='Request path ranking' valueLabel='Path' />
            <div className='grid grid-cols-1 gap-4 xl:grid-cols-2'>
                <RankingTable
                    items={ipsByRequests}
                    title='Unique IPs by requests'
                    valueLabel='Client IP'
                />
                <RankingTable
                    items={ipsByTraffic}
                    title='Unique IPs by traffic'
                    valueLabel='Client IP'
                />
            </div>

            <div className='grid grid-cols-1 gap-4 sm:grid-cols-2'>
                <ContentCard title='Traffic definition'>
                    <div className='space-y-3 text-sm text-muted'>
                        <div className='flex items-center gap-2'>
                            <Activity className='h-4 w-4 text-primary' />
                            Traffic is all request bytes received plus response bytes sent.
                        </div>
                        <div className='flex items-center gap-2'>
                            <HardDrive className='h-4 w-4 text-success' />
                            Cache traffic is response bytes served directly from edge cache.
                        </div>
                    </div>
                </ContentCard>
            </div>
        </div>
    );
}
