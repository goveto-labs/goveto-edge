import type { Summary, TopItem, TrafficPoint } from '@/api';

import { Button, Input, Label } from '@heroui/react';
import { ArrowDown, ArrowUp, MousePointerClick, Percent, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, analyticsApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatCard } from '@/components/StatCard.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

function formatBytes(bytes: number) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return `${Number((bytes / k ** i).toFixed(2))} ${sizes[i]}`;
}

function toLocalInput(date: Date) {
    const offset = date.getTimezoneOffset() * 60000;
    return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

export default function Analytics() {
    const { clusterId } = useCluster();
    const api = useMemo(() => analyticsApi(clusterId), [clusterId]);

    const [siteId, setSiteId] = useState('');
    const [from, setFrom] = useState(() =>
        toLocalInput(new Date(Date.now() - 24 * 60 * 60 * 1000))
    );
    const [to, setTo] = useState(() => toLocalInput(new Date()));
    const [summary, setSummary] = useState<Summary | null>(null);
    const [traffic, setTraffic] = useState<TrafficPoint[]>([]);
    const [topUrls, setTopUrls] = useState<TopItem[]>([]);
    const [topIps, setTopIps] = useState<TopItem[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const params = useMemo(
        () => ({
            site_id: siteId,
            from,
            to,
        }),
        [siteId, from, to]
    );

    const load = useCallback(async () => {
        if (!clusterId) return;
        setLoading(true);
        try {
            const [s, t, urls, ips] = await Promise.all([
                api.summary(params),
                api.traffic({ ...params, period: '24h' }),
                api.topUrls({ ...params, limit: 10 }),
                api.topIps({ ...params, limit: 10 }),
            ]);
            setSummary(s);
            setTraffic(t.series);
            setTopUrls(urls);
            setTopIps(ips);
            setError('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load analytics');
        } finally {
            setLoading(false);
        }
    }, [api, clusterId, params]);

    useEffect(() => {
        load();
    }, [load]);

    const chartWidth = 800;
    const chartHeight = 200;
    const padding = 24;
    const maxRequests = useMemo(() => Math.max(1, ...traffic.map((p) => p.requests)), [traffic]);

    const points = useMemo(() => {
        if (traffic.length === 0) return [];
        const innerWidth = chartWidth - padding * 2;
        const innerHeight = chartHeight - padding * 2;
        return traffic.map((point, index) => {
            const x = padding + (index / (traffic.length - 1)) * innerWidth;
            const y = chartHeight - padding - (point.requests / maxRequests) * innerHeight;
            return { x, y, value: point.requests };
        });
    }, [traffic, maxRequests]);

    const pathD = useMemo(() => {
        if (points.length === 0) return '';
        return points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${p.x} ${p.y}`).join(' ');
    }, [points]);

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='Traffic and cache performance metrics.' title='Analytics' />
                <ContentCard className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to view analytics.
                    </div>
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader subtitle='Traffic and cache performance metrics.' title='Analytics' />

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <ContentCard title='Filters'>
                <div className='flex flex-wrap items-end gap-4'>
                    <div className='flex flex-col gap-1'>
                        <Label htmlFor='analytics-site-id'>Site ID (optional)</Label>
                        <Input
                            id='analytics-site-id'
                            className='w-64'
                            variant='secondary'
                            value={siteId}
                            onChange={(e) => setSiteId(e.target.value)}
                        />
                    </div>
                    <div className='flex flex-col gap-1'>
                        <Label htmlFor='analytics-from'>From</Label>
                        <Input
                            id='analytics-from'
                            type='datetime-local'
                            variant='secondary'
                            value={from}
                            onChange={(e) => setFrom(e.target.value)}
                        />
                    </div>
                    <div className='flex flex-col gap-1'>
                        <Label htmlFor='analytics-to'>To</Label>
                        <Input
                            id='analytics-to'
                            type='datetime-local'
                            variant='secondary'
                            value={to}
                            onChange={(e) => setTo(e.target.value)}
                        />
                    </div>
                    <Button isDisabled={loading} onPress={load}>
                        <RefreshCw className='mr-2 h-4 w-4' />
                        {loading ? 'Loading...' : 'Refresh'}
                    </Button>
                </div>
            </ContentCard>

            {summary && (
                <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4'>
                    <StatCard
                        color='primary'
                        icon={MousePointerClick}
                        label='Requests'
                        value={summary.requests.toLocaleString()}
                    />
                    <StatCard
                        color='default'
                        icon={ArrowDown}
                        label='Ingress bytes'
                        value={formatBytes(summary.ingress_bytes)}
                    />
                    <StatCard
                        color='default'
                        icon={ArrowUp}
                        label='Egress bytes'
                        value={formatBytes(summary.egress_bytes)}
                    />
                    <StatCard
                        color='success'
                        icon={Percent}
                        label='Hit rate'
                        value={`${(summary.hit_rate * 100).toFixed(2)}%`}
                    />
                </div>
            )}

            <ContentCard title='Traffic (requests)'>
                {traffic.length === 0 ? (
                    <div className='text-sm text-muted'>No traffic data.</div>
                ) : (
                    <svg
                        aria-label='Traffic requests chart'
                        className='w-full'
                        height={chartHeight}
                        preserveAspectRatio='none'
                        role='img'
                        viewBox={`0 0 ${chartWidth} ${chartHeight}`}
                    >
                        <title>Traffic requests</title>
                        <line
                            stroke='currentColor'
                            strokeOpacity={0.2}
                            x1={padding}
                            x2={chartWidth - padding}
                            y1={chartHeight - padding}
                            y2={chartHeight - padding}
                        />
                        <path d={pathD} fill='none' stroke='currentColor' strokeWidth={2} />
                        {points.map((p) => (
                            <circle
                                key={`${p.x}-${p.value}`}
                                cx={p.x}
                                cy={p.y}
                                fill='currentColor'
                                r={3}
                            />
                        ))}
                    </svg>
                )}
            </ContentCard>

            <div className='grid grid-cols-1 gap-4 lg:grid-cols-2'>
                <DataTable
                    empty={topUrls.length === 0}
                    emptyDescription='URL traffic rankings will appear after requests are collected.'
                    emptyTitle='No URL traffic yet'
                    title='Top URLs'
                >
                    <thead>
                        <tr className='border-b border-border'>
                            <th className='py-3 text-left text-xs font-medium text-muted'>URL</th>
                            <th className='py-3 text-left text-xs font-medium text-muted'>
                                Requests
                            </th>
                            <th className='py-3 text-left text-xs font-medium text-muted'>Bytes</th>
                        </tr>
                    </thead>
                    <tbody>
                        {topUrls.map((item: TopItem) => (
                            <tr className='border-b border-border last:border-0' key={item.value}>
                                <td className='max-w-xs truncate py-3 text-sm'>{item.value}</td>
                                <td className='py-3 text-sm'>{item.requests.toLocaleString()}</td>
                                <td className='py-3 text-sm text-muted'>
                                    {formatBytes(item.traffic_bytes)}
                                </td>
                            </tr>
                        ))}
                    </tbody>
                </DataTable>

                <DataTable
                    empty={topIps.length === 0}
                    emptyDescription='Client IP rankings will appear after requests are collected.'
                    emptyTitle='No IP traffic yet'
                    title='Top IPs'
                >
                    <thead>
                        <tr className='border-b border-border'>
                            <th className='py-3 text-left text-xs font-medium text-muted'>IP</th>
                            <th className='py-3 text-left text-xs font-medium text-muted'>
                                Requests
                            </th>
                            <th className='py-3 text-left text-xs font-medium text-muted'>Bytes</th>
                        </tr>
                    </thead>
                    <tbody>
                        {topIps.map((item: TopItem) => (
                            <tr className='border-b border-border last:border-0' key={item.value}>
                                <td className='py-3 text-sm'>{item.value}</td>
                                <td className='py-3 text-sm'>{item.requests.toLocaleString()}</td>
                                <td className='py-3 text-sm text-muted'>
                                    {formatBytes(item.traffic_bytes)}
                                </td>
                            </tr>
                        ))}
                    </tbody>
                </DataTable>
            </div>
        </div>
    );
}
