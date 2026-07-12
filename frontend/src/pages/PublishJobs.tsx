import type { PublishJob, PublishStatus, PublishTask } from '@/api';

import { Button, Card, Chip, Input, Table } from '@heroui/react';
import { Plus, RotateCw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, publishApi } from '@/api';
import { PageHeader } from '@/components/PageHeader.tsx';
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

    useEffect(() => {
        loadStatus();
    }, [loadStatus]);

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
                <Card className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to view publish jobs.
                    </div>
                </Card>
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
                <Card className='p-5'>
                    <div className='text-sm text-muted'>State</div>
                    <div className='mt-1 text-xl font-semibold'>{status?.state ?? '-'}</div>
                </Card>
                <Card className='p-5'>
                    <div className='text-sm text-muted'>Pending</div>
                    <div className='mt-1 text-xl font-semibold'>{status?.pending_count ?? 0}</div>
                </Card>
                <Card className='p-5'>
                    <div className='text-sm text-muted'>Running</div>
                    <div className='mt-1 text-xl font-semibold'>{status?.running_count ?? 0}</div>
                </Card>
                <Card className='p-5'>
                    <div className='text-sm text-muted'>Failed</div>
                    <div className='mt-1 text-xl font-semibold text-danger'>
                        {status?.failed_count ?? 0}
                    </div>
                </Card>
            </div>

            <Card className='p-5'>
                <div className='mb-4 text-sm font-medium'>Enqueue publish</div>
                <div className='flex flex-col gap-3 sm:flex-row'>
                    <Input
                        className='flex-1'
                        placeholder='Site ID'
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
            </Card>

            <div className='space-y-3'>
                <div className='text-sm font-medium'>Recent tasks</div>
                <Card className='overflow-hidden'>
                    <Table>
                        <Table.Header>
                            <Table.Column>Task</Table.Column>
                            <Table.Column>Status</Table.Column>
                            <Table.Column>Updated</Table.Column>
                        </Table.Header>
                        <Table.Body>
                            {recentTasks.map((task) => (
                                <Table.Row key={task.id} id={task.id}>
                                    <Table.Cell className='font-mono text-xs'>{task.id}</Table.Cell>
                                    <Table.Cell>
                                        <Chip
                                            color={
                                                task.status === 'failed'
                                                    ? 'danger'
                                                    : task.status === 'completed'
                                                      ? 'success'
                                                      : 'warning'
                                            }
                                            size='sm'
                                            variant='soft'
                                        >
                                            {task.status}
                                        </Chip>
                                    </Table.Cell>
                                    <Table.Cell>{task.updated_at ?? task.created_at}</Table.Cell>
                                </Table.Row>
                            ))}
                        </Table.Body>
                    </Table>
                </Card>
            </div>

            <div className='space-y-3'>
                <div className='text-sm font-medium'>Site jobs{loading && ' (loading)'}</div>
                <Card className='overflow-hidden'>
                    <Table>
                        <Table.Header>
                            <Table.Column>Job ID</Table.Column>
                            <Table.Column>Site ID</Table.Column>
                            <Table.Column>Status</Table.Column>
                            <Table.Column>Created</Table.Column>
                        </Table.Header>
                        <Table.Body>
                            {jobs.map((job) => (
                                <Table.Row key={job.id} id={job.id}>
                                    <Table.Cell className='font-mono text-xs'>{job.id}</Table.Cell>
                                    <Table.Cell className='font-mono text-xs'>
                                        {job.site_id}
                                    </Table.Cell>
                                    <Table.Cell>{job.status}</Table.Cell>
                                    <Table.Cell>{job.created_at}</Table.Cell>
                                </Table.Row>
                            ))}
                        </Table.Body>
                    </Table>
                </Card>
            </div>
        </div>
    );
}
