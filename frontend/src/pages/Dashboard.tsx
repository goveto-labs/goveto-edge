import type { Certificate, Node, PublishStatus } from '@/api';

import { Button, Card, Spinner } from '@heroui/react';
import { ArrowRight, Files, Globe, Rocket, Server, ShieldCheck } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, certificatesApi, nodesApi, publishApi } from '@/api';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatCard } from '@/components/StatCard.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

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

    const publishFooter = status && (
        <div className='flex gap-3'>
            <span>Pending {status.pending_count}</span>
            <span>Running {status.running_count}</span>
            <span className='text-danger'>Failed {status.failed_count}</span>
        </div>
    );

    return (
        <div className='space-y-6'>
            <PageHeader subtitle='Overview of your edge cluster.' title='Dashboard' />

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
                <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3'>
                    <StatCard
                        color='primary'
                        footer={`${nodes.length} registered`}
                        icon={Server}
                        label='Nodes'
                        value={nodes.length}
                    />
                    <StatCard
                        color='success'
                        footer={`${certs.length} uploaded`}
                        icon={ShieldCheck}
                        label='Certificates'
                        value={certs.length}
                    />
                    <StatCard
                        color='warning'
                        footer={publishFooter}
                        icon={Rocket}
                        label='Publish state'
                        value={status?.state ?? '-'}
                    />
                </div>
            )}

            <Card className='p-5'>
                <div className='mb-4 flex items-center gap-2 text-sm font-medium'>
                    <Files className='h-4 w-4 text-muted' />
                    Quick actions
                </div>
                <div className='grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4'>
                    {[
                        { label: 'Manage nodes', path: '/nodes', icon: Server },
                        { label: 'Manage sites', path: '/sites', icon: Globe },
                        { label: 'Manage certificates', path: '/certificates', icon: ShieldCheck },
                        { label: 'Publish status', path: '/publish', icon: Rocket },
                    ].map((action) => (
                        <Button
                            key={action.path}
                            className='h-auto justify-between px-4 py-3'
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
            </Card>
        </div>
    );
}
