import type {
    ClusterRegion,
    DistributionItem,
    DNSLine,
    MonitoringOverview,
    Node,
    NodeRuntimePoint,
    TrafficPoint,
} from '@/api';

import { Button, Input, Label } from '@heroui/react';
import {
    Activity,
    CalendarDays,
    Download,
    Gauge,
    HardDrive,
    MousePointerClick,
    RefreshCw,
} from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';

import { ApiError, analyticsApi, clusterApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
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

function percentile(values: number[], fraction: number) {
    if (values.length === 0) return 0;
    const sorted = [...values].sort((left, right) => left - right);
    return sorted[Math.max(0, Math.ceil(sorted.length * fraction) - 1)];
}

function formatBandwidth(bytes: number, bucketSeconds: number) {
    const bitsPerSecond = (bytes * 8) / bucketSeconds;
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
    const unit = Math.min(
        Math.max(0, Math.floor(Math.log(Math.max(1, bitsPerSecond)) / Math.log(1000))),
        units.length - 1
    );
    return `${(bitsPerSecond / 1000 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function aggregateNodeMemberships(
    usage: DistributionItem[],
    nodes: Node[],
    labels: Map<string, string>,
    memberships: (node: Node) => string[]
) {
    const nodeById = new Map(nodes.map((node) => [node.id, node]));
    const totals = new Map<string, DistributionItem>();
    for (const item of usage) {
        const node = nodeById.get(item.value);
        const groupIds = node ? memberships(node) : [];
        const assignedGroups = groupIds.length > 0 ? groupIds : [''];
        for (const groupId of assignedGroups) {
            const value = groupId ? labels.get(groupId) || groupId : 'Unassigned';
            const current = totals.get(value) ?? {
                value,
                requests: 0,
                ingress_bytes: 0,
                egress_bytes: 0,
            };
            current.requests += item.requests;
            current.ingress_bytes += item.ingress_bytes;
            current.egress_bytes += item.egress_bytes;
            totals.set(value, current);
        }
    }
    return [...totals.values()].sort((left, right) => trafficOf(right) - trafficOf(left));
}

function csvCell(value: string | number) {
    let text = String(value);
    if (/^\s*[=+\-@]/.test(text)) text = `'${text}`;
    return `"${text.replace(/"/g, '""')}"`;
}

function downloadCsv(filename: string, rows: Array<Array<string | number>>) {
    const content = rows.map((row) => row.map(csvCell).join(',')).join('\r\n');
    const url = URL.createObjectURL(new Blob([content], { type: 'text/csv;charset=utf-8' }));
    const link = document.createElement('a');
    link.href = url;
    link.download = filename;
    document.body.append(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
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
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
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
    const [countries, setCountries] = useState<DistributionItem[]>([]);
    const [clientRegions, setClientRegions] = useState<DistributionItem[]>([]);
    const [nodeUsage, setNodeUsage] = useState<DistributionItem[]>([]);
    const [clusterRegions, setClusterRegions] = useState<ClusterRegion[]>([]);
    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const load = useCallback(
        async (signal?: AbortSignal) => {
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
                    countryData,
                    clientRegionData,
                    nodeUsageData,
                    clusterRegionData,
                    dnsLineData,
                ] = await Promise.all([
                    api.overview({ site_id: siteId }, { signal }),
                    api.traffic({ site_id: siteId, period }, { signal }),
                    nodeApi.list({ signal }),
                    api.rankings('domain', { ...params, sort: 'requests' }, { signal }),
                    api.distributions('extension', { ...params, sort: 'requests' }, { signal }),
                    api.distributions('status', { ...params, sort: 'requests' }, { signal }),
                    api.distributions('method', { ...params, sort: 'requests' }, { signal }),
                    api.rankings('path', { ...params, sort: 'requests' }, { signal }),
                    api.rankings('ip', { ...params, sort: 'requests' }, { signal }),
                    api.rankings('ip', { ...params, sort: 'traffic' }, { signal }),
                    api.rankings('country', { ...params, sort: 'traffic', limit: 100 }, { signal }),
                    api.rankings('region', { ...params, sort: 'traffic', limit: 100 }, { signal }),
                    api.rankings('node', { ...params, sort: 'traffic', limit: 100 }, { signal }),
                    cluster.regions({ signal }),
                    cluster.dnsLines({ signal }),
                ]);
                if (signal?.aborted) return;
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
                setCountries(countryData);
                setClientRegions(clientRegionData);
                setNodeUsage(nodeUsageData);
                setClusterRegions(clusterRegionData);
                setDnsLines(dnsLineData);
                setError('');
            } catch (loadError) {
                if (signal?.aborted) return;
                setError(
                    loadError instanceof ApiError
                        ? loadError.message
                        : 'Failed to load monitoring data'
                );
            } finally {
                if (!signal?.aborted) setLoading(false);
            }
        },
        [api, cluster, clusterId, nodeApi, period, siteId]
    );

    useAutoRefresh(load, Boolean(clusterId));

    const loadRuntime = useCallback(
        async (signal?: AbortSignal) => {
            if (!clusterId || !selectedNode) {
                setRuntime([]);
                return;
            }
            try {
                const result = await api.nodeRuntime(
                    { node_id: selectedNode, period: '12h' },
                    { signal }
                );
                if (signal?.aborted) return;
                setRuntime(result.series);
            } catch (runtimeError) {
                if (signal?.aborted) return;
                setError(
                    runtimeError instanceof ApiError
                        ? runtimeError.message
                        : 'Failed to load node trends'
                );
            }
        },
        [api, clusterId, selectedNode]
    );

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
    const bucketSeconds = period === '24h' ? 60 * 60 : 24 * 60 * 60;
    const trafficBuckets = traffic.map(totalTraffic);
    const p95Traffic = percentile(trafficBuckets, 0.95);
    const peakTraffic = Math.max(0, ...trafficBuckets);
    const periodTraffic = trafficBuckets.reduce((sum, current) => sum + current, 0);
    const deliveryRegions = useMemo(
        () =>
            aggregateNodeMemberships(
                nodeUsage,
                nodes,
                new Map(clusterRegions.map((region) => [region.id, region.name])),
                (node) => node.regionMemberships?.map((item) => item.regionId) ?? []
            ),
        [clusterRegions, nodeUsage, nodes]
    );
    const deliveryLines = useMemo(
        () =>
            aggregateNodeMemberships(
                nodeUsage,
                nodes,
                new Map(dnsLines.map((line) => [line.id, line.name])),
                (node) => node.dnsLines?.map((item) => item.dnsLineId) ?? []
            ),
        [dnsLines, nodeUsage, nodes]
    );

    const exportUsage = () => {
        const rows: Array<Array<string | number>> = [
            [
                'section',
                'bucket',
                'dimension',
                'value',
                'requests',
                'ingress_bytes',
                'egress_bytes',
                'cache_egress_bytes',
            ],
        ];
        for (const point of traffic) {
            rows.push([
                'timeseries',
                point.bucket,
                '',
                '',
                point.requests,
                point.ingress_bytes,
                point.egress_bytes,
                point.cache_egress_bytes,
            ]);
        }
        const appendRanking = (dimension: string, items: DistributionItem[]) => {
            for (const item of items) {
                rows.push([
                    'breakdown',
                    '',
                    dimension,
                    item.value,
                    item.requests,
                    item.ingress_bytes,
                    item.egress_bytes,
                    '',
                ]);
            }
        };
        appendRanking('client_country', countries);
        appendRanking('client_region', clientRegions);
        appendRanking('delivery_region', deliveryRegions);
        appendRanking('dns_line', deliveryLines);
        appendRanking('node', nodeUsage);
        const date = new Date().toISOString().slice(0, 10);
        downloadCsv(`goveto-usage-${clusterId}-${period}-${date}.csv`, rows);
    };

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
                <div className='flex flex-wrap gap-2'>
                    <Button
                        isDisabled={loading || traffic.length === 0}
                        variant='secondary'
                        onPress={exportUsage}
                    >
                        <Download className='mr-2 h-4 w-4' />
                        Export CSV
                    </Button>
                    <Button isDisabled={loading} onPress={() => void load()}>
                        <RefreshCw className='mr-2 h-4 w-4' />
                        {loading ? 'Refreshing…' : 'Refresh'}
                    </Button>
                </div>
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

            <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3'>
                <StatCard
                    color='primary'
                    footer={`${period === '24h' ? 'Hourly' : 'Daily'} buckets; not 5-minute billing P95`}
                    icon={Gauge}
                    label={`${period} P95 average bandwidth`}
                    value={formatBandwidth(p95Traffic, bucketSeconds)}
                />
                <StatCard
                    footer={`Largest ${period === '24h' ? 'hour' : 'day'} in the selected period`}
                    icon={Activity}
                    label='Peak bucket traffic'
                    value={formatBytes(peakTraffic)}
                />
                <StatCard
                    color='success'
                    footer={`${traffic.reduce((sum, point) => sum + point.requests, 0).toLocaleString()} requests`}
                    icon={CalendarDays}
                    label={`${period} total traffic`}
                    value={formatBytes(periodTraffic)}
                />
            </div>

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
                    <SelectField
                        ariaLabel='Node trend selection'
                        className='min-w-48'
                        options={nodes.map((node) => ({ id: node.id, label: node.name }))}
                        placeholder='Select a node'
                        value={selectedNode}
                        onChange={setSelectedNode}
                    />
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
                <RankingTable
                    items={countries}
                    title='Client traffic by country'
                    valueLabel='Country'
                />
                <RankingTable
                    items={clientRegions}
                    title='Client traffic by region'
                    valueLabel='Region'
                />
                <RankingTable
                    items={deliveryRegions}
                    title='Delivery traffic by node region membership'
                    valueLabel='Node region'
                />
                <RankingTable
                    items={deliveryLines}
                    title='Delivery traffic by DNS line membership'
                    valueLabel='DNS line'
                />
            </div>
            <p className='text-xs text-muted'>
                Delivery membership reports attribute a node's full usage to each region or DNS line
                it belongs to, so overlapping memberships are not additive totals.
            </p>

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
