import type { NodeRequestLog, SiteSummary } from '@/api';

import { Button, Input } from '@heroui/react';
import { RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, analyticsApi, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

export default function SitesAccessLogs() {
    const { clusterId } = useCluster();
    const sites = useMemo(() => sitesApi(clusterId), [clusterId]);
    const analytics = useMemo(() => analyticsApi(clusterId), [clusterId]);
    const [siteItems, setSiteItems] = useState<SiteSummary[]>([]);
    const [siteId, setSiteId] = useState('');
    const [logs, setLogs] = useState<NodeRequestLog[]>([]);
    const [query, setQuery] = useState('');
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const loadSites = useCallback(async () => {
        if (!clusterId) return;
        try {
            const items = await sites.list();
            setSiteItems(items);
            setSiteId((current) => current || items[0]?.id || '');
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load sites');
        }
    }, [clusterId, sites]);

    const loadLogs = useCallback(async () => {
        if (!clusterId || !siteId) {
            setLogs([]);
            return;
        }
        setLoading(true);
        try {
            setLogs(await analytics.siteLogs(siteId, 500));
            setError('');
        } catch (loadError) {
            setError(
                loadError instanceof ApiError ? loadError.message : 'Failed to load access logs'
            );
        } finally {
            setLoading(false);
        }
    }, [analytics, clusterId, siteId]);

    useEffect(() => {
        void loadSites();
    }, [loadSites]);

    useEffect(() => {
        void loadLogs();
    }, [loadLogs]);

    const filteredLogs = logs.filter((entry) =>
        `${entry.method} ${entry.hostname} ${entry.path} ${entry.status_code} ${entry.cache_status}`
            .toLowerCase()
            .includes(query.trim().toLowerCase())
    );

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='Inspect request activity by site.' title='Site access logs' />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to inspect site logs.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                subtitle='Inspect recent request activity by site.'
                title='Site access logs'
            />

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <ContentCard>
                <div className='flex flex-col gap-3 md:flex-row md:items-end'>
                    <div className='flex min-w-0 flex-1 flex-col gap-1.5'>
                        <label className='text-sm font-medium' htmlFor='site-log-site'>
                            Site
                        </label>
                        <select
                            className='rounded-lg border border-border bg-surface-secondary px-3 py-2 text-sm'
                            id='site-log-site'
                            value={siteId}
                            onChange={(event) => setSiteId(event.target.value)}
                        >
                            {siteItems.length === 0 && <option value=''>No sites available</option>}
                            {siteItems.map((site) => (
                                <option key={site.id} value={site.id}>
                                    {site.name} ({site.domains?.[0] || site.id})
                                </option>
                            ))}
                        </select>
                    </div>
                    <Input
                        aria-label='Search access logs'
                        className='min-w-0 flex-1'
                        placeholder='Search method, host, path, status…'
                        value={query}
                        variant='secondary'
                        onChange={(event) => setQuery(event.target.value)}
                    />
                    <Button isDisabled={loading || !siteId} variant='secondary' onPress={loadLogs}>
                        <RefreshCw className='mr-1.5 h-4 w-4' />
                        Refresh
                    </Button>
                </div>
            </ContentCard>

            <DataTable
                aria-label='Site access logs'
                empty={filteredLogs.length === 0}
                emptyDescription='Requests received by the selected site will appear here.'
                emptyTitle={siteId ? 'No matching access logs' : 'Select a site'}
                loading={loading}
            >
                <thead>
                    <tr>
                        <th>Time</th>
                        <th>Request</th>
                        <th>Status</th>
                        <th>Cache</th>
                        <th>Upstream</th>
                        <th>Duration</th>
                    </tr>
                </thead>
                <tbody>
                    {filteredLogs.map((entry) => (
                        <tr key={`${entry.event_time}-${entry.request_id}`}>
                            <td className='whitespace-nowrap text-sm text-muted'>
                                {new Date(entry.event_time).toLocaleString()}
                            </td>
                            <td className='max-w-lg'>
                                <div className='text-sm font-medium'>
                                    {entry.method} {entry.hostname}
                                </div>
                                <div className='truncate font-mono text-xs text-muted'>
                                    {entry.path}
                                </div>
                            </td>
                            <td className='font-mono text-sm'>{entry.status_code}</td>
                            <td className='text-sm'>{entry.cache_status || '-'}</td>
                            <td className='font-mono text-xs'>{entry.upstream_address || '-'}</td>
                            <td className='whitespace-nowrap text-sm'>
                                {(entry.duration_us / 1000).toFixed(1)} ms
                            </td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
        </div>
    );
}
