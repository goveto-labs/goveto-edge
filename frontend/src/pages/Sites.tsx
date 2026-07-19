import type { SiteSummary } from '@/api';

import { Button } from '@heroui/react';
import { Eye, Globe2, Plus } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';

function formatBandwidth(bitsPerSecond: number) {
    if (!Number.isFinite(bitsPerSecond) || bitsPerSecond <= 0) return '0 bps';
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
    const unit = Math.min(Math.floor(Math.log(bitsPerSecond) / Math.log(1000)), units.length - 1);
    return `${(bitsPerSecond / 1000 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

export default function Sites() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const api = useMemo(() => sitesApi(clusterId), [clusterId]);
    const [sites, setSites] = useState<SiteSummary[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const loadSites = useCallback(async () => {
        if (!clusterId) return;
        setLoading(true);
        try {
            setSites(await api.list());
            setError('');
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load sites');
        } finally {
            setLoading(false);
        }
    }, [api, clusterId]);

    useAutoRefresh(loadSites, Boolean(clusterId));

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Manage domains, origins, and delivery policies.'
                    title='Sites'
                />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to manage sites.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader subtitle='Manage domains, origins, and delivery policies.' title='Sites'>
                <Button onPress={() => navigate('/sites/create')}>
                    <Plus className='mr-2 h-4 w-4' />
                    Create site
                </Button>
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <DataTable
                aria-label='Sites'
                empty={sites.length === 0}
                emptyAction={
                    <Button onPress={() => navigate('/sites/create')}>
                        <Plus className='mr-2 h-4 w-4' />
                        Create site
                    </Button>
                }
                emptyDescription='Create a site to route domains to origin servers.'
                emptyTitle='No sites yet'
                loading={loading}
            >
                <thead>
                    <tr>
                        <th>Name</th>
                        <th>Domains</th>
                        <th>Status</th>
                        <th>Bandwidth</th>
                        <th>QPS</th>
                        <th>Certificates</th>
                        <th>Updated</th>
                        <th className='text-right'>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    {sites.map((site) => (
                        <tr key={site.id}>
                            <td>
                                <button
                                    className='flex items-center gap-2 text-sm font-semibold hover:underline'
                                    type='button'
                                    onClick={() => navigate(`/sites/${site.id}/overview`)}
                                >
                                    <Globe2 className='h-4 w-4 text-muted' />
                                    {site.name}
                                </button>
                            </td>
                            <td className='max-w-sm'>
                                <span className='line-clamp-2 text-sm text-muted'>
                                    {(site.domains ?? []).join(', ') || '-'}
                                </span>
                            </td>
                            <td>
                                <StatusBadge status={site.status} />
                            </td>
                            <td className='whitespace-nowrap text-sm'>
                                {formatBandwidth(site.bandwidth_bps)}
                            </td>
                            <td className='whitespace-nowrap text-sm'>
                                {site.qps.toFixed(site.qps >= 10 ? 0 : 2)}
                            </td>
                            <td className='text-sm text-muted'>{site.certificate_count}</td>
                            <td className='whitespace-nowrap text-sm text-muted'>
                                {new Date(site.updated_at).toLocaleString()}
                            </td>
                            <td>
                                <div className='flex justify-end'>
                                    <Button
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => navigate(`/sites/${site.id}/overview`)}
                                    >
                                        <Eye className='mr-1.5 h-3.5 w-3.5' /> View
                                    </Button>
                                </div>
                            </td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
        </div>
    );
}
