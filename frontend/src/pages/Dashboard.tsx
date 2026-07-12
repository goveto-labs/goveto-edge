import type { Certificate, Node, PublishStatus } from '@/api';

import { Button, Card, Chip, Spinner } from '@heroui/react';
import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, certificatesApi, nodesApi, publishApi } from '@/api';
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

    if (loading) {
        return (
            <div className='flex h-64 items-center justify-center'>
                <Spinner />
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <div className='flex items-center justify-between'>
                <h1 className='text-2xl font-bold'>Dashboard</h1>
                <Chip color='default' variant='soft'>
                    {clusterId || 'No cluster'}
                </Chip>
            </div>

            {error && (
                <div className='rounded-md bg-danger p-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3'>
                <Card className='p-4'>
                    <div className='text-sm text-muted'>Nodes</div>
                    <div className='text-3xl font-bold'>{nodes.length}</div>
                </Card>
                <Card className='p-4'>
                    <div className='text-sm text-muted'>Certificates</div>
                    <div className='text-3xl font-bold'>{certs.length}</div>
                </Card>
                <Card className='p-4'>
                    <div className='text-sm text-muted'>Publish state</div>
                    <div className='text-xl font-semibold'>{status?.state ?? '-'}</div>
                    <div className='mt-1 flex gap-2 text-xs text-muted'>
                        <span>Pending {status?.pending_count ?? 0}</span>
                        <span>Running {status?.running_count ?? 0}</span>
                        <span>Failed {status?.failed_count ?? 0}</span>
                    </div>
                </Card>
            </div>

            <div className='flex flex-wrap gap-2'>
                <Button onPress={() => navigate('/nodes')}>Manage nodes</Button>
                <Button onPress={() => navigate('/sites')}>Manage sites</Button>
                <Button onPress={() => navigate('/certificates')}>Manage certificates</Button>
                <Button onPress={() => navigate('/publish')}>Publish status</Button>
            </div>
        </div>
    );
}
