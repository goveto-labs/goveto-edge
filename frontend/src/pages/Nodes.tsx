import type {
    ClusterGroup,
    ClusterRegion,
    DNSLine,
    Node,
    NodeAddress,
    NodeCacheConfig,
} from '@/api';

import {
    Button,
    Card,
    Drawer,
    Input,
    Label,
    ListBox,
    Modal,
    Select,
    Spinner,
    Switch,
    Table,
    useOverlayState,
} from '@heroui/react';
import { Eye, Plus, Power, PowerOff, Save, Trash2 } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, clusterApi, nodesApi } from '@/api';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

function parseCommaList(value: string) {
    return value
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
}

export default function Nodes() {
    const { clusterId } = useCluster();
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
    const nodeApi = useMemo(() => nodesApi(clusterId), [clusterId]);
    const [nodes, setNodes] = useState<Node[]>([]);
    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [groups, setGroups] = useState<ClusterGroup[]>([]);
    const [regions, setRegions] = useState<ClusterRegion[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const createState = useOverlayState();
    const [createLoading, setCreateLoading] = useState(false);
    const [createError, setCreateError] = useState('');
    const [name, setName] = useState('');
    const [addresses, setAddresses] = useState('');
    const [dnsLineIds, setDnsLineIds] = useState<Set<string>>(new Set());
    const [groupId, setGroupId] = useState('');
    const [regionId, setRegionId] = useState('');
    const [sshIp, setSshIp] = useState('');
    const [sshPort, setSshPort] = useState('22');
    const [sshUser, setSshUser] = useState('');
    const [sshPassword, setSshPassword] = useState('');
    const [sshKey, setSshKey] = useState('');
    const [sshPassphrase, setSshPassphrase] = useState('');

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
        setLoading(true);
        try {
            const [n, d, g, r] = await Promise.all([
                nodeApi.list(),
                cluster.dnsLines(),
                cluster.groups(),
                cluster.regions(),
            ]);
            setNodes(n);
            setDnsLines(d);
            setGroups(g);
            setRegions(r);
            setError('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load nodes');
        } finally {
            setLoading(false);
        }
    }, [cluster, nodeApi, clusterId]);

    useEffect(() => {
        load();
    }, [load]);

    const openCreate = () => {
        setName('');
        setAddresses('');
        setDnsLineIds(new Set());
        setGroupId('');
        setRegionId('');
        setSshIp('');
        setSshPort('22');
        setSshUser('');
        setSshPassword('');
        setSshKey('');
        setSshPassphrase('');
        setCreateError('');
        createState.open();
    };

    const handleCreate = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!clusterId) return;
        setCreateLoading(true);
        setCreateError('');
        try {
            await nodeApi.create({
                name,
                addresses: parseCommaList(addresses),
                dns_line_ids: Array.from(dnsLineIds),
                group_id: groupId || undefined,
                region_id: regionId || undefined,
                ssh: {
                    entry_ip: sshIp,
                    port: Number(sshPort) || 22,
                    user: sshUser,
                    password: sshPassword || undefined,
                    private_key: sshKey || undefined,
                    passphrase: sshPassphrase || undefined,
                },
            });
            createState.close();
            await load();
        } catch (err) {
            setCreateError(err instanceof ApiError ? err.message : 'Failed to create node');
        } finally {
            setCreateLoading(false);
        }
    };

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
        setNodeActionLoading(true); setDetailError('');
        try {
            await nodeApi.updateDNSLines(selectedNodeId, Array.from(detailDNSLineIds));
            await openDetail(selectedNodeId); await load();
        } catch (err) { setDetailError(err instanceof ApiError ? err.message : 'Failed to update DNS lines'); }
        finally { setNodeActionLoading(false); }
    };

    const setNodeEnabled = async (enabled: boolean) => {
        if (!selectedNodeId) return;
        setNodeActionLoading(true); setDetailError('');
        try {
            if (enabled) await nodeApi.enable(selectedNodeId); else await nodeApi.disable(selectedNodeId);
            await openDetail(selectedNodeId); await load();
        } catch (err) { setDetailError(err instanceof ApiError ? err.message : 'Failed to change node status'); }
        finally { setNodeActionLoading(false); }
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
                <Card className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to manage nodes.
                    </div>
                </Card>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader subtitle='Manage edge nodes and their cache configuration.' title='Nodes'>
                <Button onPress={openCreate}>
                    <Plus className='mr-2 h-4 w-4' />
                    Create node
                </Button>
            </PageHeader>

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
                <Card className='overflow-hidden'>
                    <Table>
                        <Table.ScrollContainer>
                            <Table.Content aria-label='Nodes'>
                                <Table.Header>
                                    <Table.Column isRowHeader>Name</Table.Column>
                                    <Table.Column>Status</Table.Column>
                                    <Table.Column>DNS lines</Table.Column>
                                    <Table.Column>Addresses</Table.Column>
                                    <Table.Column>Actions</Table.Column>
                                </Table.Header>
                                <Table.Body>
                                    {nodes.map((node) => (
                                        <Table.Row key={node.id} id={node.id}>
                                            <Table.Cell className='font-medium'>
                                                {node.name}
                                            </Table.Cell>
                                            <Table.Cell>{node.status}</Table.Cell>
                                            <Table.Cell>
                                                {(node.dnsLines || [])
                                                    .map(
                                                        (link) =>
                                                            dnsLines.find(
                                                                (line) =>
                                                                    line.id === link.dnsLineId,
                                                            )?.name || link.dnsLineId,
                                                    )
                                                    .join(', ') || 'Default'}
                                            </Table.Cell>
                                            <Table.Cell>
                                                {node.addresses.map((a) => a.address).join(', ')}
                                            </Table.Cell>
                                            <Table.Cell>
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
                                            </Table.Cell>
                                        </Table.Row>
                                    ))}
                                </Table.Body>
                            </Table.Content>
                        </Table.ScrollContainer>
                    </Table>
                </Card>
            )}

            <Modal isOpen={createState.isOpen} onOpenChange={createState.setOpen}>
                <Modal.Backdrop>
                    <Modal.Container size='md'>
                        <Modal.Dialog>
                            <form className='space-y-4' onSubmit={handleCreate}>
                                <Modal.Header>
                                    <Modal.Heading>Create node</Modal.Heading>
                                </Modal.Header>
                                <Modal.Body>
                                    {createError && (
                                        <div className='rounded-md bg-danger p-3 text-sm text-danger-foreground'>
                                            {createError}
                                        </div>
                                    )}
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='node-name'>Name</Label>
                                        <Input
                                            variant='secondary'
                                            id='node-name'
                                            required
                                            value={name}
                                            onChange={(e) => setName(e.target.value)}
                                        />
                                    </div>
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='node-addresses'>
                                            Addresses (comma separated)
                                        </Label>
                                        <Input
                                            variant='secondary'
                                            id='node-addresses'
                                            required
                                            value={addresses}
                                            onChange={(e) => setAddresses(e.target.value)}
                                        />
                                    </div>
                                    <Select
                                        variant='secondary'
                                        value={Array.from(dnsLineIds)}
                                        selectionMode='multiple'
                                        onChange={(keys) =>
                                            setDnsLineIds(new Set(keys as string[]))
                                        }
                                    >
                                        <Label>DNS lines</Label>
                                        <Select.Trigger>
                                            <Select.Value />
                                        </Select.Trigger>
                                        <Select.Popover>
                                            <ListBox>
                                                {dnsLines.map((line) => (
                                                    <ListBox.Item
                                                        key={line.id}
                                                        id={line.id}
                                                        textValue={line.name}
                                                    >
                                                        {line.name}
                                                    </ListBox.Item>
                                                ))}
                                            </ListBox>
                                        </Select.Popover>
                                    </Select>
                                    <Select
                                        variant='secondary'
                                        value={groupId || null}
                                        onChange={(key) => setGroupId(String(key ?? ''))}
                                    >
                                        <Label>Group (optional)</Label>
                                        <Select.Trigger>
                                            <Select.Value />
                                        </Select.Trigger>
                                        <Select.Popover>
                                            <ListBox>
                                                {groups.map((g) => (
                                                    <ListBox.Item
                                                        key={g.id}
                                                        id={g.id}
                                                        textValue={g.name}
                                                    >
                                                        {g.name}
                                                    </ListBox.Item>
                                                ))}
                                            </ListBox>
                                        </Select.Popover>
                                    </Select>
                                    <Select
                                        variant='secondary'
                                        value={regionId || null}
                                        onChange={(key) => setRegionId(String(key ?? ''))}
                                    >
                                        <Label>Region (optional)</Label>
                                        <Select.Trigger>
                                            <Select.Value />
                                        </Select.Trigger>
                                        <Select.Popover>
                                            <ListBox>
                                                {regions.map((r) => (
                                                    <ListBox.Item
                                                        key={r.id}
                                                        id={r.id}
                                                        textValue={r.name}
                                                    >
                                                        {r.name}
                                                    </ListBox.Item>
                                                ))}
                                            </ListBox>
                                        </Select.Popover>
                                    </Select>
                                    <div className='grid grid-cols-2 gap-4'>
                                        <div className='flex flex-col gap-1'>
                                            <Label htmlFor='node-ssh-ip'>SSH entry IP</Label>
                                            <Input
                                                variant='secondary'
                                                id='node-ssh-ip'
                                                required
                                                value={sshIp}
                                                onChange={(e) => setSshIp(e.target.value)}
                                            />
                                        </div>
                                        <div className='flex flex-col gap-1'>
                                            <Label htmlFor='node-ssh-port'>SSH port</Label>
                                            <Input
                                                variant='secondary'
                                                id='node-ssh-port'
                                                required
                                                type='number'
                                                value={sshPort}
                                                onChange={(e) => setSshPort(e.target.value)}
                                            />
                                        </div>
                                    </div>
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='node-ssh-user'>SSH user</Label>
                                        <Input
                                            variant='secondary'
                                            id='node-ssh-user'
                                            required
                                            value={sshUser}
                                            onChange={(e) => setSshUser(e.target.value)}
                                        />
                                    </div>
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='node-ssh-password'>SSH password</Label>
                                        <Input
                                            variant='secondary'
                                            id='node-ssh-password'
                                            type='password'
                                            value={sshPassword}
                                            onChange={(e) => setSshPassword(e.target.value)}
                                        />
                                    </div>
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='node-ssh-key'>SSH private key path</Label>
                                        <Input
                                            variant='secondary'
                                            id='node-ssh-key'
                                            value={sshKey}
                                            onChange={(e) => setSshKey(e.target.value)}
                                        />
                                    </div>
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='node-ssh-passphrase'>
                                            SSH key passphrase
                                        </Label>
                                        <Input
                                            variant='secondary'
                                            id='node-ssh-passphrase'
                                            type='password'
                                            value={sshPassphrase}
                                            onChange={(e) => setSshPassphrase(e.target.value)}
                                        />
                                    </div>
                                </Modal.Body>
                                <Modal.Footer>
                                    <Button type='button' variant='ghost' onPress={createState.close}>
                                        Cancel
                                    </Button>
                                    <Button isDisabled={createLoading} type='submit' variant='primary'>
                                        {createLoading ? 'Creating...' : 'Create'}
                                    </Button>
                                </Modal.Footer>
                            </form>
                        </Modal.Dialog>
                    </Modal.Container>
                </Modal.Backdrop>
            </Modal>

            <Drawer isOpen={drawerState.isOpen} onOpenChange={drawerState.setOpen}>
                <Drawer.Content className='w-full max-w-lg'>
                    <Drawer.Header>
                        <Drawer.Heading>Node details</Drawer.Heading>
                    </Drawer.Header>
                    <Drawer.Body>
                        {detailLoading && <Spinner />}
                        {detailError && (
                            <div className='rounded-md bg-danger p-3 text-sm text-danger-foreground'>
                                {detailError}
                            </div>
                        )}
                        {selectedNode && (
                            <div className='space-y-6'>
                                <div className='space-y-1'>
                                    <div className='text-sm text-muted'>Name</div>
                                    <div className='font-medium'>{selectedNode.name}</div>
                                </div>
                                <div className='space-y-1'>
                                    <div className='text-sm text-muted'>Status</div>
                                    <div className='flex items-center justify-between gap-3'>
                                        <div className='font-medium'>{selectedNode.status}</div>
                                        {selectedNode.status === 'DISABLED' ? (
                                            <Button isDisabled={nodeActionLoading} size='sm' onPress={() => setNodeEnabled(true)}><Power className='mr-1.5 h-4 w-4' />Enable</Button>
										) : (selectedNode.status === 'ONLINE' || selectedNode.status === 'OFFLINE' || selectedNode.status === 'INSTALL_FAILED') ? (
											<Button isDisabled={nodeActionLoading} size='sm' variant='danger' onPress={() => setNodeEnabled(false)}><PowerOff className='mr-1.5 h-4 w-4' />Disable</Button>
										) : null}
                                    </div>
                                </div>

                                <div className='space-y-2 border-t border-border pt-4'>
                                    <Select
                                        variant='secondary'
                                        selectionMode='multiple'
                                        value={Array.from(detailDNSLineIds)}
                                        onChange={(keys) =>
                                            setDetailDNSLineIds(new Set(keys as string[]))
                                        }
                                    >
                                        <Label>Regional DNS lines</Label>
                                        <Select.Trigger>
                                            <Select.Value />
                                        </Select.Trigger>
                                        <Select.Popover>
                                            <ListBox>
                                                {dnsLines.map((line) => (
                                                    <ListBox.Item
                                                        key={line.id}
                                                        id={line.id}
                                                        textValue={`${line.name} ${line.providerCode}`}
                                                    >
                                                        {line.name} ({line.providerCode})
                                                    </ListBox.Item>
                                                ))}
                                            </ListBox>
                                        </Select.Popover>
                                    </Select>
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
                                <div className='space-y-1'>
                                    <div className='text-sm text-muted'>Addresses</div>
                                    <div className='space-y-1'>
                                        {selectedNode.addresses.map((addr: NodeAddress) => (
                                            <div key={addr.id} className='text-sm'>
                                                {addr.address} {addr.primary && '(primary)'}
                                            </div>
                                        ))}
                                    </div>
                                </div>

                                <div className='space-y-2'>
                                    <div className='text-sm font-medium'>Add address</div>
                                    <div className='flex items-center gap-2'>
                                        <Input
                                            variant='secondary'
                                            className='flex-1'
                                            aria-label='New address'
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
                                        <div className='text-sm font-medium'>Cache config</div>
                                        <div className='flex flex-col gap-1'>
                                            <Label htmlFor='node-cache-dir'>Cache directory</Label>
                                            <Input
                                                variant='secondary'
                                                id='node-cache-dir'
                                                value={cache.cache_directory}
                                                onChange={(e) =>
                                                    setCache({
                                                        ...cache,
                                                        cache_directory: e.target.value,
                                                    })
                                                }
                                            />
                                        </div>
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
                                        <div className='flex flex-col gap-1'>
                                            <Label htmlFor='node-cache-max-size'>
                                                Max size bytes
                                            </Label>
                                            <Input
                                                variant='secondary'
                                                id='node-cache-max-size'
                                                type='number'
                                                value={String(cache.max_size_bytes)}
                                                onChange={(e) =>
                                                    setCache({
                                                        ...cache,
                                                        max_size_bytes: Number(e.target.value),
                                                    })
                                                }
                                            />
                                        </div>
                                        <div className='flex flex-col gap-1'>
                                            <Label htmlFor='node-cache-disk-usage'>
                                                Max disk usage percent
                                            </Label>
                                            <Input
                                                variant='secondary'
                                                id='node-cache-disk-usage'
                                                type='number'
                                                value={String(cache.max_disk_usage_percent)}
                                                onChange={(e) =>
                                                    setCache({
                                                        ...cache,
                                                        max_disk_usage_percent: Number(
                                                            e.target.value,
                                                        ),
                                                    })
                                                }
                                            />
                                        </div>
                                        <Button isDisabled={cacheSaving} onPress={saveCache}>
                                            {cacheSaving ? 'Saving...' : 'Save cache config'}
                                        </Button>
                                    </div>
                                )}
                            </div>
                        )}
                    </Drawer.Body>
                    <Drawer.Footer>
                        <Button onPress={drawerState.close}>Close</Button>
                    </Drawer.Footer>
                </Drawer.Content>
            </Drawer>
        </div>
    );
}
