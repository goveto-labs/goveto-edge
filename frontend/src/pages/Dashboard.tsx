import type { Certificate, Node, PublishStatus, PublishTask } from '@/api';

import { Button } from '@heroui/react';
import { ArrowRight, Globe, Rocket, Server, ShieldCheck } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, certificatesApi, nodesApi, publishApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatCard } from '@/components/StatCard.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

function formatDateTime(value?: string) {
    if (!value) return '-';
    return new Date(value).toLocaleString(undefined, {
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
    });
}

function taskTarget(task: PublishTask) {
    if (task.site_id) return `Site ${task.site_id.slice(0, 8)}`;
    if (task.node_id) return `Node ${task.node_id.slice(0, 8)}`;
    return '-';
}

export default function Dashboard() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const [nodes, setNodes] = useState<Node[]>([]);
    const [certs, setCerts] = useState<Certificate[]>([]);
    const [status, setStatus] = useState<PublishStatus | null>(null);
    const [error, setError] = useState('');

    useEffect(() => {
        if (!clusterId) return;
        Promise.all([
            nodesApi(clusterId).list(),
            certificatesApi(clusterId).list(),
            publishApi(clusterId).status(),
        ])
            .then(([n, c, s]) => {
                setNodes(n);
                setCerts(c);
                setStatus(s);
                setError('');
            })
            .catch((err) => {
                setError(err instanceof ApiError ? err.message : 'Failed to load dashboard');
            });
    }, [clusterId]);

    const stats = useMemo(
        () => [
            {
                label: 'Nodes',
                value: nodes.length,
                footer: `${nodes.length} registered`,
                icon: Server,
                color: 'primary' as const,
            },
            {
                label: 'Certificates',
                value: certs.length,
                footer: `${certs.length} uploaded`,
                icon: ShieldCheck,
                color: 'success' as const,
            },
            {
                label: 'Publish state',
                value: status?.state ?? '-',
                footer: (
                    <div className='flex gap-3'>
                        <span>Pending {status?.pending_count ?? 0}</span>
                        <span>Running {status?.running_count ?? 0}</span>
                        <span className='text-danger'>Failed {status?.failed_count ?? 0}</span>
                    </div>
                ),
                icon: Rocket,
                color: 'warning' as const,
            },
            {
                label: 'Sites',
                value: '-',
                footer: 'Manage sites to publish configs',
                icon: Globe,
                color: 'default' as const,
            },
        ],
        [nodes.length, certs.length, status]
    );

    const quickActions = [
        { label: 'Manage nodes', path: '/nodes', icon: Server },
        { label: 'Manage sites', path: '/sites', icon: Globe },
        { label: 'Manage certificates', path: '/certificates', icon: ShieldCheck },
        { label: 'Publish status', path: '/publish', icon: Rocket },
    ];

    const recentTasks = status?.recent_tasks?.slice(0, 6) ?? [];

    return (
        <div className='space-y-6'>
            <PageHeader
                subtitle='Overview of your edge cluster.'
                tabs={[
                    { id: 'overview', label: 'Overview' },
                    { id: 'nodes', label: 'Nodes' },
                    { id: 'publish', label: 'Publish' },
                ]}
                title='Dashboard'
            />

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <section className='grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4'>
                {stats.map((stat) => (
                    <StatCard
                        key={stat.label}
                        color={stat.color}
                        footer={stat.footer}
                        icon={stat.icon}
                        label={stat.label}
                        value={stat.value}
                    />
                ))}
            </section>

            <section className='grid grid-cols-1 gap-4 lg:grid-cols-12'>
                <ContentCard className='lg:col-span-8' title='Publish activity'>
                    <DataTable
                        aria-label='Recent publish tasks'
                        className='border-0 shadow-none'
                        empty={recentTasks.length === 0}
                        emptyDescription='Publish tasks will appear after a site is deployed.'
                        emptyTitle='No recent publish tasks'
                    >
                        <thead>
                            <tr>
                                <th>Task</th>
                                <th>Target</th>
                                <th>Status</th>
                                <th>Updated</th>
                            </tr>
                        </thead>
                        <tbody>
                            {recentTasks.map((task: PublishTask) => (
                                <tr key={task.id}>
                                    <td className='font-mono text-xs'>{task.id.slice(0, 12)}</td>
                                    <td className='text-sm text-muted'>{taskTarget(task)}</td>
                                    <td>
                                        <StatusBadge status={task.status} />
                                    </td>
                                    <td className='text-sm text-muted'>
                                        {formatDateTime(task.updated_at ?? task.created_at)}
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </DataTable>
                </ContentCard>

                <div className='flex flex-col gap-4 lg:col-span-4'>
                    <ContentCard
                        action={
                            <Button size='sm' variant='ghost' onPress={() => navigate('/publish')}>
                                View all
                            </Button>
                        }
                        className='flex-1'
                        title='Quick actions'
                    >
                        <div className='grid grid-cols-1 gap-1'>
                            {quickActions.map((action) => (
                                <Button
                                    key={action.path}
                                    className='h-auto justify-between px-3 py-2.5'
                                    variant='ghost'
                                    onPress={() => navigate(action.path)}
                                >
                                    <span className='flex items-center gap-2'>
                                        <action.icon className='h-4 w-4' />
                                        {action.label}
                                    </span>
                                    <ArrowRight className='h-4 w-4 text-muted' />
                                </Button>
                            ))}
                        </div>
                    </ContentCard>

                    <ContentCard title='Cluster snapshot'>
                        <dl className='space-y-3 text-sm'>
                            <div className='flex justify-between'>
                                <dt className='text-muted'>Active nodes</dt>
                                <dd className='font-medium'>{nodes.length}</dd>
                            </div>
                            <div className='flex justify-between'>
                                <dt className='text-muted'>Certificates</dt>
                                <dd className='font-medium'>{certs.length}</dd>
                            </div>
                            <div className='flex justify-between'>
                                <dt className='text-muted'>Pending tasks</dt>
                                <dd className='font-medium'>{status?.pending_count ?? 0}</dd>
                            </div>
                            <div className='flex justify-between'>
                                <dt className='text-muted'>Failed tasks</dt>
                                <dd className='font-medium text-danger'>
                                    {status?.failed_count ?? 0}
                                </dd>
                            </div>
                        </dl>
                    </ContentCard>
                </div>
            </section>
        </div>
    );
}
