import type { PublishJob, PublishStatus, PublishTask } from '@/api';

import { Button, Input } from '@heroui/react';
import { Plus, RotateCw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, publishApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatCard } from '@/components/StatCard.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';

export default function PublishJobs() {
    const { clusterId } = useCluster();
    const api = useMemo(() => publishApi(clusterId), [clusterId]);
    const [status, setStatus] = useState<PublishStatus | null>(null);
    const [recentTasks, setRecentTasks] = useState<PublishTask[]>([]);
    const [siteId, setSiteId] = useState('');
    const [jobs, setJobs] = useState<PublishJob[]>([]);
    const [loading, setLoading] = useState(false);
    const [publishing, setPublishing] = useState(false);
    const [error, setError] = useState('');

    const loadStatus = useCallback(async () => {
        if (!clusterId) return;
        try {
            const s = await api.status();
            setStatus(s);
            setRecentTasks(s.recent_tasks);
            setError('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load publish status');
        }
    }, [api, clusterId]);

    useAutoRefresh(loadStatus, Boolean(clusterId));

    useEffect(() => {
        if (!clusterId) return;
        const es = api.events();
        es.addEventListener('sync_status', (event) => {
            try {
                const data = JSON.parse(event.data) as PublishStatus;
                setStatus(data);
                setRecentTasks(data.recent_tasks);
            } catch {
                // ignore malformed events
            }
        });
        es.onerror = () => {
            // allow reconnect on next mount
        };
        return () => es.close();
    }, [api, clusterId]);

    const handlePublish = async () => {
        if (!siteId.trim()) return;
        setPublishing(true);
        try {
            const job = await api.enqueueSite(siteId.trim());
            setJobs((prev) => [job, ...prev]);
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to publish site');
        } finally {
            setPublishing(false);
        }
    };

    const loadSiteJobs = async () => {
        if (!siteId.trim()) return;
        setLoading(true);
        try {
            const data = await api.listSiteJobs(siteId.trim());
            setJobs(data);
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load jobs');
        } finally {
            setLoading(false);
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Publish configuration changes to edge nodes.'
                    title='Publish Jobs'
                />
                <ContentCard className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to view publish jobs.
                    </div>
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                subtitle='Publish configuration changes to edge nodes.'
                title='Publish Jobs'
            />

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4'>
                <StatCard
                    footer='Current publish state'
                    icon={RotateCw}
                    label='State'
                    value={status?.state ?? '-'}
                />
                <StatCard
                    color='warning'
                    footer='Tasks waiting'
                    icon={Plus}
                    label='Pending'
                    value={status?.pending_count ?? 0}
                />
                <StatCard
                    color='primary'
                    footer='Tasks in progress'
                    icon={RotateCw}
                    label='Running'
                    value={status?.running_count ?? 0}
                />
                <StatCard
                    color='danger'
                    footer='Tasks that failed'
                    icon={RotateCw}
                    label='Failed'
                    value={status?.failed_count ?? 0}
                />
            </div>

            <ContentCard title='Enqueue publish'>
                <div className='flex flex-col gap-3 sm:flex-row'>
                    <Input
                        aria-label='Site ID'
                        className='flex-1'
                        placeholder='Site ID'
                        variant='secondary'
                        value={siteId}
                        onChange={(e) => setSiteId(e.target.value)}
                    />
                    <div className='flex gap-2'>
                        <Button isDisabled={publishing} onPress={handlePublish}>
                            <Plus className='mr-2 h-4 w-4' />
                            {publishing ? 'Publishing...' : 'Publish'}
                        </Button>
                        <Button variant='ghost' onPress={loadSiteJobs}>
                            <RotateCw className='mr-2 h-4 w-4' />
                            Load jobs
                        </Button>
                    </div>
                </div>
            </ContentCard>

            <DataTable
                empty={recentTasks.length === 0}
                emptyDescription='Publishing activity will appear here.'
                emptyTitle='No recent tasks'
                title='Recent tasks'
            >
                <thead>
                    <tr className='border-b border-border'>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Task</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Status</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Updated</th>
                    </tr>
                </thead>
                <tbody>
                    {recentTasks.map((task: PublishTask) => (
                        <tr className='border-b border-border last:border-0' key={task.id}>
                            <td className='py-3 font-mono text-xs'>{task.id}</td>
                            <td className='py-3'>
                                <StatusBadge status={task.status} />
                            </td>
                            <td className='py-3 text-sm text-muted'>
                                {task.updated_at ?? task.created_at}
                            </td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>

            <DataTable
                empty={jobs.length === 0}
                emptyDescription='Choose a site and load its publish history.'
                emptyTitle='No site jobs loaded'
                loading={loading && jobs.length === 0}
                title='Site jobs'
            >
                <thead>
                    <tr className='border-b border-border'>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Job ID</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Site ID</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Status</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Created</th>
                    </tr>
                </thead>
                <tbody>
                    {jobs.map((job: PublishJob) => (
                        <tr className='border-b border-border last:border-0' key={job.id}>
                            <td className='py-3 font-mono text-xs'>{job.id}</td>
                            <td className='py-3 font-mono text-xs text-muted'>{job.site_id}</td>
                            <td className='py-3'>
                                <StatusBadge status={job.status} />
                            </td>
                            <td className='py-3 text-sm text-muted'>{job.created_at}</td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
        </div>
    );
}
