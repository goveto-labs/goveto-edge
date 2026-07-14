import type { Certificate, Node, PublishStatus, PublishTask } from '@/api';

import { Button, Spinner } from '@heroui/react';
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

function formatDate(value?: string) {
    if (!value) return '-';
    return new Date(value).toLocaleDateString();
}

export default function Dashboard() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const [nodes, setNodes] = useState<Node[]>([]);
    const [certs, setCerts] = useState<Certificate[]>([]);
    const [status, setStatus] = useState<PublishStatus | null>(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    useEffect(() => {
        if (!clusterId) return;
        setLoading(true);
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
            })
            .finally(() => setLoading(false));
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

    const recentTasks = status?.recent_tasks?.slice(0, 5) ?? [];

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

            {loading ? (
                <div className='flex h-64 items-center justify-center'>
                    <Spinner />
                </div>
            ) : (
                <>
                    <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4'>
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
                    </div>

                    <div className='grid grid-cols-1 gap-4 lg:grid-cols-3'>
                        <ContentCard className='lg:col-span-2' title='Publish activity'>
                            {recentTasks.length === 0 ? (
                                <div className='text-sm text-muted'>No recent publish tasks.</div>
                            ) : (
                                <DataTable aria-label='Recent publish tasks'>
                                    <thead>
                                        <tr className='border-b border-border'>
                                            <th className='py-2 text-left text-xs font-medium text-muted'>
                                                Task
                                            </th>
                                            <th className='py-2 text-left text-xs font-medium text-muted'>
                                                Status
                                            </th>
                                            <th className='py-2 text-left text-xs font-medium text-muted'>
                                                Updated
                                            </th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {recentTasks.map((task: PublishTask) => (
                                            <tr
                                                className='border-b border-border last:border-0'
                                                key={task.id}
                                            >
                                                <td className='py-3 font-mono text-xs'>
                                                    {task.id}
                                                </td>
                                                <td className='py-3'>
                                                    <StatusBadge status={task.status} />
                                                </td>
                                                <td className='py-3 text-sm text-muted'>
                                                    {formatDate(task.updated_at ?? task.created_at)}
                                                </td>
                                            </tr>
                                        ))}
                                    </tbody>
                                </DataTable>
                            )}
                        </ContentCard>

                        <ContentCard
                            action={
                                <Button
                                    size='sm'
                                    variant='ghost'
                                    onPress={() => navigate('/publish')}
                                >
                                    View all
                                </Button>
                            }
                            title='Quick actions'
                        >
                            <div className='grid grid-cols-1 gap-2'>
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
                    </div>
                </>
            )}
        </div>
    );
}
