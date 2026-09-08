import type { DNSLine, Node, NodeSnapshot } from '@/api';

import { Button, Input } from '@heroui/react';
import { Check, Eye, Globe2, Plus, Power, PowerOff, Search, Trash2 } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, analyticsApi, clusterApi, nodesApi } from '@/api';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { TablePagination } from '@/components/TablePagination.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { useListQuery } from '@/hooks/useListQuery.ts';
import { enumField, integerField, stringField } from '@/utils/listQuery.ts';
import { matchesQuery, paginateItems } from '@/utils/pagination.ts';
import { canManageCluster } from '@/utils/rbac.ts';

const nodesQuery = {
    q: stringField(),
    status: enumField(
        ['', 'ONLINE', 'OFFLINE', 'DISABLED', 'INSTALLING', 'INSTALL_FAILED', 'PENDING'] as const,
        ''
    ),
    page: integerField(1, { min: 1 }),
    page_size: integerField(25, { allowed: [25, 50, 100] }),
};

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
    const { clusterId, clusters } = useCluster();
    const canManage = canManageCluster(
        clusters.find((clusterItem) => clusterItem.id === clusterId)?.role
    );
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
    const nodeApi = useMemo(() => nodesApi(clusterId), [clusterId]);
    const analytics = useMemo(() => analyticsApi(clusterId), [clusterId]);
    const [nodes, setNodes] = useState<Node[]>([]);
    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [runtimeByNode, setRuntimeByNode] = useState<Record<string, NodeSnapshot>>({});
    const [error, setError] = useState('');
    const [editingNode, setEditingNode] = useState<Node | null>(null);
    const [pendingDelete, setPendingDelete] = useState<Node | null>(null);
    const [pendingDisable, setPendingDisable] = useState<Node | null>(null);
    const [statusUpdatingNodeId, setStatusUpdatingNodeId] = useState<string | null>(null);
    const [dnsLineDraft, setDnsLineDraft] = useState<Set<string>>(new Set());
    const [dnsLineQuery, setDnsLineQuery] = useState('');
    const [dnsLineSaving, setDnsLineSaving] = useState(false);
    const [dnsLineError, setDnsLineError] = useState('');
    const { values, replace, searchInput, setSearchInput } = useListQuery(nodesQuery, {
        searchKey: 'q',
    });

    const filteredDnsLines = useMemo(() => {
        const query = dnsLineQuery.trim().toLocaleLowerCase();
        if (!query) return dnsLines;
        return dnsLines.filter((line) =>
            `${line.name} ${line.providerCode}`.toLocaleLowerCase().includes(query)
        );
    }, [dnsLineQuery, dnsLines]);

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

    useAutoRefresh(load, Boolean(clusterId));

    const filteredNodes = useMemo(
        () =>
            nodes.filter((node) => {
                if (values.status && node.status !== values.status) return false;
                return matchesQuery(
                    values.q,
                    node.name,
                    node.id,
                    node.addresses.map((address) => address.address)
                );
            }),
        [nodes, values.q, values.status]
    );
    const pagedNodes = paginateItems(filteredNodes, values.page, values.page_size);
    useEffect(() => {
        if (pagedNodes.page !== values.page) replace({ page: pagedNodes.page });
    }, [pagedNodes.page, replace, values.page]);

    const handleDelete = async (node: Node) => {
        try {
            await nodeApi.delete(node.id);
            await load();
        } catch (deleteError) {
            setError(
                deleteError instanceof ApiError ? deleteError.message : 'Failed to delete node'
            );
        }
    };

    const handleToggleStatus = async (node: Node) => {
        setStatusUpdatingNodeId(node.id);
        try {
            const response =
                node.status === 'DISABLED'
                    ? await nodeApi.enable(node.id)
                    : await nodeApi.disable(node.id);
            setNodes((current) =>
                current.map((item) =>
                    item.id === node.id ? { ...item, status: response.status } : item
                )
            );
        } catch (statusError) {
            setError(
                statusError instanceof ApiError
                    ? statusError.message
                    : 'Failed to update node status'
            );
        } finally {
            setStatusUpdatingNodeId(null);
        }
    };

    const openDnsLineEditor = (node: Node) => {
        setEditingNode(node);
        setDnsLineDraft(new Set((node.dnsLines || []).map((line) => line.dnsLineId)));
        setDnsLineQuery('');
        setDnsLineError('');
    };

    const closeDnsLineEditor = () => {
        if (dnsLineSaving) return;
        setEditingNode(null);
        setDnsLineQuery('');
        setDnsLineError('');
    };

    const toggleDnsLine = (dnsLineId: string) => {
        setDnsLineDraft((current) => {
            const next = new Set(current);
            if (next.has(dnsLineId)) next.delete(dnsLineId);
            else next.add(dnsLineId);
            return next;
        });
    };

    const saveDnsLines = async () => {
        if (!editingNode) return;
        setDnsLineSaving(true);
        setDnsLineError('');
        try {
            const response = await nodeApi.updateDNSLines(editingNode.id, Array.from(dnsLineDraft));
            setNodes((current) =>
                current.map((node) =>
                    node.id === response.node_id
                        ? {
                              ...node,
                              dnsLines: response.dns_line_ids.map((dnsLineId) => ({
                                  nodeId: response.node_id,
                                  dnsLineId,
                              })),
                          }
                        : node
                )
            );
            setEditingNode(null);
        } catch (saveError) {
            setDnsLineError(
                saveError instanceof ApiError ? saveError.message : 'Failed to update DNS lines'
            );
        } finally {
            setDnsLineSaving(false);
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
                {canManage && (
                    <Button onPress={() => navigate('/nodes/create')}>
                        <Plus className='mr-2 h-4 w-4' />
                        Create node
                    </Button>
                )}
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <DataTable
                aria-label='Nodes'
                action={
                    <div className='grid w-full gap-2 sm:grid-cols-2 lg:grid-cols-[minmax(220px,1fr)_160px_110px]'>
                        <Input
                            aria-label='Search nodes'
                            placeholder='Search name or address'
                            value={searchInput}
                            variant='secondary'
                            onChange={(event) => setSearchInput(event.target.value)}
                        />
                        <SelectField
                            ariaLabel='Node status'
                            options={[
                                { id: '', label: 'All statuses' },
                                { id: 'ONLINE', label: 'Online' },
                                { id: 'OFFLINE', label: 'Offline' },
                                { id: 'DISABLED', label: 'Disabled' },
                                { id: 'INSTALLING', label: 'Installing' },
                                { id: 'INSTALL_FAILED', label: 'Install failed' },
                                { id: 'PENDING', label: 'Pending' },
                            ]}
                            value={values.status}
                            variant='secondary'
                            onChange={(value) =>
                                replace(
                                    { status: value as typeof values.status },
                                    { resetPage: true }
                                )
                            }
                        />
                        <SelectField
                            ariaLabel='Rows per page'
                            options={[25, 50, 100].map((size) => ({
                                id: String(size),
                                label: `${size} rows`,
                            }))}
                            value={String(values.page_size)}
                            variant='secondary'
                            onChange={(value) =>
                                replace({ page_size: Number(value) }, { resetPage: true })
                            }
                        />
                    </div>
                }
                caption='Nodes'
                empty={pagedNodes.items.length === 0}
                emptyAction={
                    canManage && nodes.length === 0 ? (
                        <Button onPress={() => navigate('/nodes/create')}>
                            <Plus className='mr-2 h-4 w-4' />
                            Create node
                        </Button>
                    ) : undefined
                }
                emptyDescription={
                    nodes.length === 0
                        ? 'Create a node to start serving traffic from this cluster.'
                        : 'No nodes match the current filters.'
                }
                emptyTitle={nodes.length === 0 ? 'No nodes yet' : 'No matching nodes'}
                footer={
                    <TablePagination
                        page={pagedNodes.page}
                        pageSize={values.page_size}
                        total={pagedNodes.total}
                        onPageChange={(nextPage) => replace({ page: nextPage })}
                    />
                }
                title={`${pagedNodes.total.toLocaleString()} nodes`}
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
                    {pagedNodes.items.map((node) => {
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
                                    <div className='flex items-center gap-2'>
                                        <span className='line-clamp-2 min-w-0'>
                                            {assignedLines.join(', ') || 'Default'}
                                        </span>
                                        {canManage && (
                                            <button
                                                aria-label={`Edit DNS lines for ${node.name}`}
                                                className='shrink-0 font-mono text-xs font-semibold text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40'
                                                type='button'
                                                onClick={() => openDnsLineEditor(node)}
                                            >
                                                [EDIT]
                                            </button>
                                        )}
                                    </div>
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
                                        {canManage && node.status === 'DISABLED' && (
                                            <Button
                                                isDisabled={statusUpdatingNodeId === node.id}
                                                size='sm'
                                                variant='secondary'
                                                onPress={() => void handleToggleStatus(node)}
                                            >
                                                <Power className='mr-1.5 h-3.5 w-3.5' />
                                                {statusUpdatingNodeId === node.id
                                                    ? 'Enabling…'
                                                    : 'Enable'}
                                            </Button>
                                        )}
                                        {canManage &&
                                            (node.status === 'ONLINE' ||
                                                node.status === 'OFFLINE' ||
                                                node.status === 'INSTALL_FAILED') && (
                                                <Button
                                                    isDisabled={statusUpdatingNodeId === node.id}
                                                    size='sm'
                                                    variant='secondary'
                                                    onPress={() => setPendingDisable(node)}
                                                >
                                                    <PowerOff className='mr-1.5 h-3.5 w-3.5' />
                                                    Disable
                                                </Button>
                                            )}
                                        {canManage && (
                                            <Button
                                                size='sm'
                                                variant='danger'
                                                onPress={() => setPendingDelete(node)}
                                            >
                                                <Trash2 className='mr-1.5 h-3.5 w-3.5' />
                                                Delete
                                            </Button>
                                        )}
                                    </div>
                                </td>
                            </tr>
                        );
                    })}
                </tbody>
            </DataTable>

            <DialogShell
                icon={<Globe2 className='h-4 w-4' />}
                isDismissable={!dnsLineSaving}
                isOpen={Boolean(editingNode)}
                size='md'
                subtitle={
                    editingNode
                        ? `Choose the routing lines assigned to ${editingNode.name}.`
                        : undefined
                }
                title='Configure DNS lines'
                onOpenChange={(open) => {
                    if (!open) closeDnsLineEditor();
                }}
            >
                <div className='space-y-4 px-6 py-5'>
                    {dnsLineError && (
                        <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                            {dnsLineError}
                        </div>
                    )}

                    <div className='relative'>
                        <Search className='pointer-events-none absolute left-3 top-1/2 z-10 h-4 w-4 -translate-y-1/2 text-muted' />
                        <Input
                            autoFocus
                            aria-label='Search DNS lines'
                            className='pl-9'
                            placeholder='Search by name or provider code…'
                            value={dnsLineQuery}
                            variant='secondary'
                            onChange={(event) => setDnsLineQuery(event.target.value)}
                        />
                    </div>

                    <div className='max-h-80 overflow-y-auto rounded-xl border border-border bg-surface-secondary/20'>
                        {filteredDnsLines.length === 0 ? (
                            <div className='px-6 py-10 text-center text-sm text-muted'>
                                {dnsLines.length === 0
                                    ? 'No DNS lines are configured.'
                                    : 'No DNS lines match the current search.'}
                            </div>
                        ) : (
                            filteredDnsLines.map((line) => {
                                const selected = dnsLineDraft.has(line.id);
                                return (
                                    <button
                                        aria-pressed={selected}
                                        className={`flex w-full items-center gap-3 border-b border-border px-4 py-3 text-left transition-colors last:border-b-0 hover:bg-surface-secondary ${
                                            selected ? 'bg-accent/10' : 'bg-surface'
                                        }`}
                                        key={line.id}
                                        type='button'
                                        onClick={() => toggleDnsLine(line.id)}
                                    >
                                        <span
                                            className={`flex h-5 w-5 shrink-0 items-center justify-center rounded border ${
                                                selected
                                                    ? 'border-primary bg-primary text-primary-foreground'
                                                    : 'border-border bg-surface'
                                            }`}
                                        >
                                            {selected && <Check className='h-3.5 w-3.5' />}
                                        </span>
                                        <span className='min-w-0 flex-1'>
                                            <span className='block truncate text-sm font-medium text-foreground'>
                                                {line.name}
                                            </span>
                                            <span className='block truncate text-xs text-muted'>
                                                {line.providerCode}
                                            </span>
                                        </span>
                                        {selected && (
                                            <span className='text-xs font-medium text-primary'>
                                                Selected
                                            </span>
                                        )}
                                    </button>
                                );
                            })
                        )}
                    </div>

                    <p className='text-sm text-muted'>
                        Leave all lines unselected to use default routing.
                    </p>
                </div>
                <DialogFooter>
                    <div className='mr-auto self-center text-sm text-muted'>
                        {dnsLineDraft.size} selected
                    </div>
                    <Button
                        isDisabled={dnsLineSaving}
                        type='button'
                        variant='ghost'
                        onPress={closeDnsLineEditor}
                    >
                        Cancel
                    </Button>
                    <Button
                        isDisabled={dnsLineSaving}
                        type='button'
                        onPress={() => void saveDnsLines()}
                    >
                        {dnsLineSaving ? 'Saving…' : 'Save changes'}
                    </Button>
                </DialogFooter>
            </DialogShell>

            <ConfirmDialog
                confirmLabel='Disable'
                danger
                description={pendingDisable ? `Disable node "${pendingDisable.name}"?` : undefined}
                impact='The node is removed from DNS scheduling and stops serving managed traffic.'
                isOpen={pendingDisable !== null}
                recoverability='Recoverable. Enable the node to return it to rotation.'
                title='Disable node?'
                onConfirm={() => {
                    const node = pendingDisable;
                    setPendingDisable(null);
                    if (node) void handleToggleStatus(node);
                }}
                onOpenChange={(open) => {
                    if (!open) setPendingDisable(null);
                }}
            />

            <ConfirmDialog
                confirmLabel='Delete'
                confirmationText={pendingDelete?.name}
                danger
                description={pendingDelete ? `Delete node "${pendingDelete.name}"?` : undefined}
                impact='One edge node will stop receiving configuration and serving managed traffic.'
                isOpen={pendingDelete !== null}
                recoverability='Not recoverable. The node must be added and installed again.'
                title='Delete node?'
                onConfirm={() => {
                    const node = pendingDelete;
                    setPendingDelete(null);
                    if (node) void handleDelete(node);
                }}
                onOpenChange={(open) => {
                    if (!open) setPendingDelete(null);
                }}
            />
        </div>
    );
}
