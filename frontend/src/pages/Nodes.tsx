import type { DNSLine, Node, NodeAddress, NodeCacheConfig } from '@/api';

import {
    Button,
    Drawer,
    Input,
    ListBox,
    Select,
    Spinner,
    Switch,
    useOverlayState,
} from '@heroui/react';
import { Eye, Plus, Power, PowerOff, Save, Server, Trash2 } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, clusterApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

export default function Nodes() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
    const nodeApi = useMemo(() => nodesApi(clusterId), [clusterId]);
    const [nodes, setNodes] = useState<Node[]>([]);
    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [error, setError] = useState('');

    const drawerState = useOverlayState();
    const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
    const [selectedNode, setSelectedNode] = useState<Node | null>(null);
    const [detailLoading, setDetailLoading] = useState(false);
    const [detailError, setDetailError] = useState('');
    const [cache, setCache] = useState<NodeCacheConfig | null>(null);
    const [cacheSaving, setCacheSaving] = useState(false);
    const [newAddress, setNewAddress] = useState('');
    const [newAddressPrimary, setNewAddressPrimary] = useState(false);
    const [detailDNSLineIds, setDetailDNSLineIds] = useState<Set<string>>(new Set());
    const [nodeActionLoading, setNodeActionLoading] = useState(false);

    const load = useCallback(async () => {
        if (!clusterId) return;
        try {
            const [n, d] = await Promise.all([nodeApi.list(), cluster.dnsLines()]);
            setNodes(n);
            setDnsLines(d);
            setError('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load nodes');
        }
    }, [cluster, nodeApi, clusterId]);

    useEffect(() => {
        load();
    }, [load]);

    const handleDelete = async (nodeId: string) => {
        if (!confirm('Delete this node?')) return;
        try {
            await nodeApi.delete(nodeId);
            await load();
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to delete node');
        }
    };

    const openDetail = async (nodeId: string) => {
        setSelectedNodeId(nodeId);
        setDetailError('');
        setDetailLoading(true);
        drawerState.open();
        try {
            const node = await nodeApi.get(nodeId);
            setSelectedNode(node);
            setDetailDNSLineIds(new Set((node.dnsLines || []).map((line) => line.dnsLineId)));
            const cfg = node.cacheConfig ?? (await nodeApi.getCacheConfig(nodeId));
            setCache(cfg);
        } catch (err) {
            setDetailError(err instanceof ApiError ? err.message : 'Failed to load node');
        } finally {
            setDetailLoading(false);
        }
    };

    const saveDNSLines = async () => {
        if (!selectedNodeId) return;
        setNodeActionLoading(true);
        setDetailError('');
        try {
            await nodeApi.updateDNSLines(selectedNodeId, Array.from(detailDNSLineIds));
            await openDetail(selectedNodeId);
            await load();
        } catch (err) {
            setDetailError(err instanceof ApiError ? err.message : 'Failed to update DNS lines');
        } finally {
            setNodeActionLoading(false);
        }
    };

    const setNodeEnabled = async (enabled: boolean) => {
        if (!selectedNodeId) return;
        setNodeActionLoading(true);
        setDetailError('');
        try {
            if (enabled) await nodeApi.enable(selectedNodeId);
            else await nodeApi.disable(selectedNodeId);
            await openDetail(selectedNodeId);
            await load();
        } catch (err) {
            setDetailError(err instanceof ApiError ? err.message : 'Failed to change node status');
        } finally {
            setNodeActionLoading(false);
        }
    };

    const saveCache = async () => {
        if (!selectedNodeId || !cache) return;
        setCacheSaving(true);
        try {
            const result = await nodeApi.updateCacheConfig(selectedNodeId, cache);
            setCache(result.cache_config);
        } catch (err) {
            setDetailError(err instanceof ApiError ? err.message : 'Failed to update cache');
        } finally {
            setCacheSaving(false);
        }
    };

    const addAddress = async () => {
        if (!selectedNodeId || !newAddress.trim()) return;
        try {
            await nodeApi.addAddress(selectedNodeId, {
                address: newAddress.trim(),
                primary: newAddressPrimary,
            });
            setNewAddress('');
            setNewAddressPrimary(false);
            if (selectedNodeId) await openDetail(selectedNodeId);
        } catch (err) {
            setDetailError(err instanceof ApiError ? err.message : 'Failed to add address');
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
            <PageHeader subtitle='Manage edge nodes and their cache configuration.' title='Nodes'>
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
                    <tr className='border-b border-border'>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Name</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Status</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>DNS lines</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Addresses</th>
                        <th className='py-3 text-right text-xs font-medium text-muted'>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    {nodes.map((node) => (
                        <tr className='border-b border-border last:border-0' key={node.id}>
                            <td className='py-3 text-sm font-medium'>{node.name}</td>
                            <td className='py-3'>
                                <StatusBadge status={node.status} />
                            </td>
                            <td className='py-3 text-sm text-muted'>
                                {(node.dnsLines || [])
                                    .map(
                                        (link) =>
                                            dnsLines.find((line) => line.id === link.dnsLineId)
                                                ?.name || link.dnsLineId
                                    )
                                    .join(', ') || 'Default'}
                            </td>
                            <td className='py-3 text-sm text-muted'>
                                {node.addresses.map((a) => a.address).join(', ')}
                            </td>
                            <td className='py-3'>
                                <div className='flex justify-end gap-2'>
                                    <Button
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => openDetail(node.id)}
                                    >
                                        <Eye className='mr-1.5 h-3.5 w-3.5' />
                                        View
                                    </Button>
                                    <Button
                                        size='sm'
                                        variant='danger'
                                        onPress={() => handleDelete(node.id)}
                                    >
                                        <Trash2 className='mr-1.5 h-3.5 w-3.5' />
                                        Delete
                                    </Button>
                                </div>
                            </td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>

            <Drawer isOpen={drawerState.isOpen} onOpenChange={drawerState.setOpen}>
                <Drawer.Content className='w-full max-w-lg'>
                    <Drawer.Header>
                        <Drawer.Heading className='flex items-center gap-2 text-lg font-semibold'>
                            <Server className='h-5 w-5 text-muted' />
                            Node details
                        </Drawer.Heading>
                    </Drawer.Header>
                    <Drawer.Body className='space-y-6'>
                        {detailLoading && <Spinner />}
                        {detailError && <FormError message={detailError} />}
                        {selectedNode && (
                            <>
                                <div className='space-y-4'>
                                    <FormField label='Name'>
                                        <div className='text-sm font-medium'>
                                            {selectedNode.name}
                                        </div>
                                    </FormField>
                                    <FormField label='Status'>
                                        <div className='flex items-center justify-between gap-3'>
                                            <StatusBadge status={selectedNode.status} />
                                            {selectedNode.status === 'DISABLED' ? (
                                                <Button
                                                    isDisabled={nodeActionLoading}
                                                    size='sm'
                                                    onPress={() => setNodeEnabled(true)}
                                                >
                                                    <Power className='mr-1.5 h-4 w-4' />
                                                    Enable
                                                </Button>
                                            ) : selectedNode.status === 'ONLINE' ||
                                              selectedNode.status === 'OFFLINE' ||
                                              selectedNode.status === 'INSTALL_FAILED' ? (
                                                <Button
                                                    isDisabled={nodeActionLoading}
                                                    size='sm'
                                                    variant='danger'
                                                    onPress={() => setNodeEnabled(false)}
                                                >
                                                    <PowerOff className='mr-1.5 h-4 w-4' />
                                                    Disable
                                                </Button>
                                            ) : null}
                                        </div>
                                    </FormField>
                                </div>

                                <div className='space-y-4 border-t border-border pt-4'>
                                    <FormField label='Regional DNS lines'>
                                        <Select
                                            selectionMode='multiple'
                                            value={Array.from(detailDNSLineIds)}
                                            variant='secondary'
                                            onChange={(keys) =>
                                                setDetailDNSLineIds(new Set(keys as string[]))
                                            }
                                        >
                                            <Select.Trigger>
                                                <Select.Value />
                                            </Select.Trigger>
                                            <Select.Popover>
                                                <ListBox>
                                                    {dnsLines.map((line) => (
                                                        <ListBox.Item
                                                            id={line.id}
                                                            key={line.id}
                                                            textValue={`${line.name} ${line.providerCode}`}
                                                        >
                                                            {line.name} ({line.providerCode})
                                                        </ListBox.Item>
                                                    ))}
                                                </ListBox>
                                            </Select.Popover>
                                        </Select>
                                    </FormField>
                                    <Button
                                        isDisabled={nodeActionLoading}
                                        size='sm'
                                        variant='secondary'
                                        onPress={saveDNSLines}
                                    >
                                        <Save className='mr-1.5 h-4 w-4' />
                                        Save DNS lines
                                    </Button>
                                </div>
                                <FormField label='Addresses'>
                                    <div className='space-y-1'>
                                        {selectedNode.addresses.map((addr: NodeAddress) => (
                                            <div className='text-sm' key={addr.id}>
                                                {addr.address} {addr.primary && '(primary)'}
                                            </div>
                                        ))}
                                    </div>
                                </FormField>

                                <div className='space-y-3 border-t border-border pt-4'>
                                    <div className='text-sm font-semibold'>Add address</div>
                                    <div className='flex items-center gap-2'>
                                        <Input
                                            variant='secondary'
                                            aria-label='New address'
                                            className='flex-1'
                                            placeholder='Address'
                                            value={newAddress}
                                            onChange={(e) => setNewAddress(e.target.value)}
                                        />
                                        <label
                                            className='flex items-center gap-2 text-sm'
                                            htmlFor='node-address-primary'
                                        >
                                            <Switch
                                                id='node-address-primary'
                                                isSelected={newAddressPrimary}
                                                onChange={(checked) =>
                                                    setNewAddressPrimary(checked)
                                                }
                                            />
                                            Primary
                                        </label>
                                        <Button size='sm' onPress={addAddress}>
                                            Add
                                        </Button>
                                    </div>
                                </div>

                                {cache && (
                                    <div className='space-y-4 border-t border-border pt-4'>
                                        <div className='flex items-center gap-2 text-sm font-semibold'>
                                            <Save className='h-4 w-4 text-muted' />
                                            Cache config
                                        </div>
                                        <FormField htmlFor='node-cache-dir' label='Cache directory'>
                                            <Input
                                                id='node-cache-dir'
                                                variant='secondary'
                                                value={cache.cache_directory}
                                                onChange={(e) =>
                                                    setCache({
                                                        ...cache,
                                                        cache_directory: e.target.value,
                                                    })
                                                }
                                            />
                                        </FormField>
                                        <label
                                            className='flex items-center gap-2 text-sm'
                                            htmlFor='node-cache-auto-max-size'
                                        >
                                            <Switch
                                                id='node-cache-auto-max-size'
                                                isSelected={cache.auto_max_size}
                                                onChange={(checked) =>
                                                    setCache({ ...cache, auto_max_size: checked })
                                                }
                                            />
                                            Auto max size
                                        </label>
                                        <div className='grid grid-cols-1 gap-4 sm:grid-cols-2'>
                                            <FormField
                                                htmlFor='node-cache-max-size'
                                                label='Max size bytes'
                                            >
                                                <Input
                                                    id='node-cache-max-size'
                                                    type='number'
                                                    variant='secondary'
                                                    value={String(cache.max_size_bytes)}
                                                    onChange={(e) =>
                                                        setCache({
                                                            ...cache,
                                                            max_size_bytes: Number(e.target.value),
                                                        })
                                                    }
                                                />
                                            </FormField>
                                            <FormField
                                                htmlFor='node-cache-disk-usage'
                                                label='Max disk usage %'
                                            >
                                                <Input
                                                    id='node-cache-disk-usage'
                                                    type='number'
                                                    variant='secondary'
                                                    value={String(cache.max_disk_usage_percent)}
                                                    onChange={(e) =>
                                                        setCache({
                                                            ...cache,
                                                            max_disk_usage_percent: Number(
                                                                e.target.value
                                                            ),
                                                        })
                                                    }
                                                />
                                            </FormField>
                                        </div>
                                        <Button isDisabled={cacheSaving} onPress={saveCache}>
                                            <Save className='mr-1.5 h-4 w-4' />
                                            {cacheSaving ? 'Saving...' : 'Save cache config'}
                                        </Button>
                                    </div>
                                )}
                            </>
                        )}
                    </Drawer.Body>
                    <Drawer.Footer className='border-t border-border'>
                        <Button variant='ghost' onPress={drawerState.close}>
                            Close
                        </Button>
                    </Drawer.Footer>
                </Drawer.Content>
            </Drawer>
        </div>
    );
}
