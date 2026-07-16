import type { DNSLine, Node, NodeSnapshot } from '@/api';

import { Button } from '@heroui/react';
import { Eye, Plus, Trash2 } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, analyticsApi, clusterApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

function formatPercent(value: number) {
    return `${value.toFixed(1)}%`;
}

function formatBytes(bytes: number) {
    if (bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const unit = Math.min(
        Math.max(0, Math.floor(Math.log(bytes) / Math.log(1024))),
        units.length - 1
    );
    return `${(bytes / 1024 ** unit).toFixed(1)} ${units[unit]}`;
}

function formatMemory(metric?: NodeSnapshot) {
    if (!metric || metric.memory_total_bytes <= 0) return '—';
    const percent = (metric.memory_used_bytes / metric.memory_total_bytes) * 100;
    return `${formatBytes(metric.memory_used_bytes)} / ${formatBytes(metric.memory_total_bytes)} (${formatPercent(percent)})`;
}

function formatRate(bytesPerSecond: number) {
    return `${formatBytes(bytesPerSecond)}/s`;
}

export default function Nodes() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
    const nodeApi = useMemo(() => nodesApi(clusterId), [clusterId]);
    const analytics = useMemo(() => analyticsApi(clusterId), [clusterId]);
    const [nodes, setNodes] = useState<Node[]>([]);
    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [runtimeByNode, setRuntimeByNode] = useState<Record<string, NodeSnapshot>>({});
    const [error, setError] = useState('');

    const load = useCallback(async () => {
        if (!clusterId) return;
        try {
            const [nodeData, lineData, runtimeData] = await Promise.all([
                nodeApi.list(),
                cluster.dnsLines(),
                analytics.latestNodeRuntime().catch(() => []),
            ]);
            setNodes(nodeData);
            setDnsLines(lineData);
            setRuntimeByNode(
                Object.fromEntries(runtimeData.map((metric) => [metric.node_id, metric]))
            );
            setError('');
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load nodes');
        }
    }, [analytics, cluster, clusterId, nodeApi]);

    useEffect(() => {
        void load();
        const refresh = window.setInterval(() => void load(), 60_000);
        return () => window.clearInterval(refresh);
    }, [load]);

    const handleDelete = async (node: Node) => {
        if (!confirm(`Delete node "${node.name}"?`)) return;
        try {
            await nodeApi.delete(node.id);
            await load();
        } catch (deleteError) {
            setError(
                deleteError instanceof ApiError ? deleteError.message : 'Failed to delete node'
            );
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Manage edge nodes and their cache configuration.'
                    title='Nodes'
                />
                <ContentCard className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to manage nodes.
                    </div>
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader subtitle='Manage edge nodes and their configuration.' title='Nodes'>
                <Button onPress={() => navigate('/nodes/create')}>
                    <Plus className='mr-2 h-4 w-4' />
                    Create node
                </Button>
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <DataTable
                aria-label='Nodes'
                empty={nodes.length === 0}
                emptyAction={
                    <Button onPress={() => navigate('/nodes/create')}>
                        <Plus className='mr-2 h-4 w-4' />
                        Create node
                    </Button>
                }
                emptyDescription='Create a node to start serving traffic from this cluster.'
                emptyTitle='No nodes yet'
            >
                <thead>
                    <tr>
                        <th>Name</th>
                        <th>Status</th>
                        <th>DNS lines</th>
                        <th>Addresses</th>
                        <th>CPU</th>
                        <th>Memory</th>
                        <th>Bandwidth</th>
                        <th>Requests/min</th>
                        <th className='text-right'>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    {nodes.map((node) => {
                        const assignedLines = (node.dnsLines || []).map(
                            (link) =>
                                dnsLines.find((line) => line.id === link.dnsLineId)?.name ||
                                link.dnsLineId
                        );
                        const runtime = runtimeByNode[node.id];
                        const runtimeTitle = runtime
                            ? `Reported ${new Date(runtime.bucket).toLocaleString()}`
                            : 'No runtime metrics have been reported';
                        return (
                            <tr key={node.id}>
                                <td>
                                    <button
                                        className='text-sm font-semibold hover:underline'
                                        type='button'
                                        onClick={() => navigate(`/nodes/${node.id}/overview`)}
                                    >
                                        {node.name}
                                    </button>
                                </td>
                                <td>
                                    <div className='flex flex-col items-start gap-1.5'>
                                        <span
                                            className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium ${
                                                runtime?.online
                                                    ? 'bg-success/10 text-success'
                                                    : 'bg-danger/10 text-danger'
                                            }`}
                                            title={runtimeTitle}
                                        >
                                            <span
                                                className={`h-1.5 w-1.5 rounded-full ${runtime?.online ? 'bg-success' : 'bg-danger'}`}
                                            />
                                            {runtime?.online ? 'Online' : 'Offline'}
                                        </span>
                                        <StatusBadge status={node.status} />
                                        {node.status === 'INSTALL_FAILED' && node.installError && (
                                            <p
                                                className='max-w-xs truncate text-xs text-danger'
                                                title={node.installError}
                                            >
                                                {node.installError}
                                            </p>
                                        )}
                                    </div>
                                </td>
                                <td className='max-w-xs text-sm text-muted'>
                                    <span className='line-clamp-2'>
                                        {assignedLines.join(', ') || 'Default'}
                                    </span>
                                </td>
                                <td className='max-w-xs text-sm text-muted'>
                                    <span className='line-clamp-2 font-mono text-xs'>
                                        {node.addresses
                                            .map((address) => address.address)
                                            .join(', ') || '—'}
                                    </span>
                                </td>
                                <td className='whitespace-nowrap text-sm' title={runtimeTitle}>
                                    {runtime ? formatPercent(runtime.cpu_usage_percent) : '—'}
                                </td>
                                <td className='whitespace-nowrap text-sm' title={runtimeTitle}>
                                    {formatMemory(runtime)}
                                </td>
                                <td className='whitespace-nowrap text-sm' title={runtimeTitle}>
                                    {runtime
                                        ? formatRate(
                                              runtime.ingress_bytes_per_second +
                                                  runtime.egress_bytes_per_second
                                          )
                                        : '—'}
                                </td>
                                <td className='text-sm' title={runtimeTitle}>
                                    {runtime?.requests_per_minute.toLocaleString() ?? '—'}
                                </td>
                                <td>
                                    <div className='flex justify-end gap-2'>
                                        <Button
                                            size='sm'
                                            variant='secondary'
                                            onPress={() => navigate(`/nodes/${node.id}/overview`)}
                                        >
                                            <Eye className='mr-1.5 h-3.5 w-3.5' />
                                            View
                                        </Button>
                                        <Button
                                            size='sm'
                                            variant='danger'
                                            onPress={() => void handleDelete(node)}
                                        >
                                            <Trash2 className='mr-1.5 h-3.5 w-3.5' />
                                            Delete
                                        </Button>
                                    </div>
                                </td>
                            </tr>
                        );
                    })}
                </tbody>
            </DataTable>
        </div>
    );
}
