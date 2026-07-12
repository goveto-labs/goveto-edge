import type { Summary, TopItem, TrafficPoint } from '@/api';

import { Button, Card, Input, Label, Table } from '@heroui/react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, analyticsApi } from '@/api';
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
            <div className='text-sm text-muted'>
                Select a cluster in the header to view analytics.
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <h1 className='text-2xl font-bold'>Analytics</h1>

            <Card className='p-4'>
                <div className='flex flex-wrap items-end gap-4'>
                    <div>
                        <Label htmlFor='analytics-site-id'>Site ID (optional)</Label>
                        <Input
                            id='analytics-site-id'
                            className='w-64'
                            value={siteId}
                            onChange={(e) => setSiteId(e.target.value)}
                        />
                    </div>
                    <div>
                        <Label htmlFor='analytics-from'>From</Label>
                        <Input
                            id='analytics-from'
                            type='datetime-local'
                            value={from}
                            onChange={(e) => setFrom(e.target.value)}
                        />
                    </div>
                    <div>
                        <Label htmlFor='analytics-to'>To</Label>
                        <Input
                            id='analytics-to'
                            type='datetime-local'
                            value={to}
                            onChange={(e) => setTo(e.target.value)}
                        />
                    </div>
                    <Button isDisabled={loading} onPress={load}>
                        {loading ? 'Loading...' : 'Refresh'}
                    </Button>
                </div>
            </Card>

            {error && (
                <div className='rounded-md bg-danger p-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            {summary && (
                <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4'>
                    <Card className='p-4'>
                        <div className='text-sm text-muted'>Requests</div>
                        <div className='text-2xl font-bold'>
                            {summary.requests.toLocaleString()}
                        </div>
                    </Card>
                    <Card className='p-4'>
                        <div className='text-sm text-muted'>Ingress bytes</div>
                        <div className='text-2xl font-bold'>
                            {formatBytes(summary.ingress_bytes)}
                        </div>
                    </Card>
                    <Card className='p-4'>
                        <div className='text-sm text-muted'>Egress bytes</div>
                        <div className='text-2xl font-bold'>
                            {formatBytes(summary.egress_bytes)}
                        </div>
                    </Card>
                    <Card className='p-4'>
                        <div className='text-sm text-muted'>Hit rate</div>
                        <div className='text-2xl font-bold'>
                            {(summary.hit_rate * 100).toFixed(2)}%
                        </div>
                    </Card>
                </div>
            )}

            <Card className='p-4'>
                <div className='mb-2 text-sm font-medium'>Traffic (requests)</div>
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
            </Card>

            <div className='grid grid-cols-1 gap-4 lg:grid-cols-2'>
                <Card className='overflow-hidden'>
                    <div className='p-3 text-sm font-medium'>Top URLs</div>
                    <Table>
                        <Table.Header>
                            <Table.Column>URL</Table.Column>
                            <Table.Column>Requests</Table.Column>
                            <Table.Column>Bytes</Table.Column>
                        </Table.Header>
                        <Table.Body>
                            {topUrls.map((item) => (
                                <Table.Row key={item.value} id={item.value}>
                                    <Table.Cell className='max-w-xs truncate'>
                                        {item.value}
                                    </Table.Cell>
                                    <Table.Cell>{item.requests.toLocaleString()}</Table.Cell>
                                    <Table.Cell>{formatBytes(item.traffic_bytes)}</Table.Cell>
                                </Table.Row>
                            ))}
                        </Table.Body>
                    </Table>
                </Card>

                <Card className='overflow-hidden'>
                    <div className='p-3 text-sm font-medium'>Top IPs</div>
                    <Table>
                        <Table.Header>
                            <Table.Column>IP</Table.Column>
                            <Table.Column>Requests</Table.Column>
                            <Table.Column>Bytes</Table.Column>
                        </Table.Header>
                        <Table.Body>
                            {topIps.map((item) => (
                                <Table.Row key={item.value} id={item.value}>
                                    <Table.Cell>{item.value}</Table.Cell>
                                    <Table.Cell>{item.requests.toLocaleString()}</Table.Cell>
                                    <Table.Cell>{formatBytes(item.traffic_bytes)}</Table.Cell>
                                </Table.Row>
                            ))}
                        </Table.Body>
                    </Table>
                </Card>
            </div>
        </div>
    );
}
