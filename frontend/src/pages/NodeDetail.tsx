import type { ClusterGroup, ClusterRegion, DNSLine, Node, NodeCacheConfig, NodeSSH } from '@/api';

import { Button, Input, Switch, TextArea } from '@heroui/react';
import {
    ArrowLeft,
    Check,
    Database,
    Globe2,
    HardDrive,
    KeyRound,
    LockKeyhole,
    MapPin,
    Network,
    Pencil,
    Plus,
    RefreshCw,
    Save,
    Server,
    Trash2,
    X,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import { ApiError, clusterApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { FormRow } from '@/components/FormRow.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SearchableMultiAddField } from '@/components/SearchableMultiAddField.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

type DetailTab = 'overview' | 'network' | 'cache' | 'reinstall';
type SSHAuthMethod = 'password' | 'private_key';

const tabs: Array<{ id: DetailTab; label: string; icon: typeof Server }> = [
    { id: 'overview', label: 'Overview', icon: Server },
    { id: 'network', label: 'Network & DNS', icon: Network },
    { id: 'cache', label: 'Cache', icon: HardDrive },
    { id: 'reinstall', label: 'Reinstall', icon: RefreshCw },
];

function SectionTitle({
    icon: Icon,
    title,
    description,
}: {
    icon: typeof Server;
    title: string;
    description: string;
}) {
    return (
        <div className='flex items-start gap-3 border-b border-border px-5 py-4'>
            <span className='flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-surface-secondary text-muted'>
                <Icon className='h-4 w-4' />
            </span>
            <div>
                <h2 className='text-sm font-semibold'>{title}</h2>
                <p className='mt-0.5 text-xs leading-5 text-muted'>{description}</p>
            </div>
        </div>
    );
}

function Metric({ label, value }: { label: string; value: React.ReactNode }) {
    return (
        <div className='rounded-xl border border-border bg-surface-secondary/30 p-4'>
            <div className='text-xs font-medium uppercase tracking-wide text-muted'>{label}</div>
            <div className='mt-2 min-h-6 text-sm font-semibold'>{value}</div>
        </div>
    );
}

function NameChips({ names, empty }: { names: string[]; empty: string }) {
    if (names.length === 0) return <span className='text-sm text-muted'>{empty}</span>;
    return (
        <div className='flex flex-wrap gap-2'>
            {names.map((name) => (
                <span
                    className='rounded-full border border-border bg-surface-secondary px-2.5 py-1 text-xs font-medium'
                    key={name}
                >
                    {name}
                </span>
            ))}
        </div>
    );
}

export default function NodeDetail() {
    const navigate = useNavigate();
    const { nodeId = '' } = useParams();
    const { clusterId } = useCluster();
    const api = useMemo(() => nodesApi(clusterId), [clusterId]);
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
    const [tab, setTab] = useState<DetailTab>('overview');
    const [node, setNode] = useState<Node | null>(null);
    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [groups, setGroups] = useState<ClusterGroup[]>([]);
    const [regions, setRegions] = useState<ClusterRegion[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');

    const [dnsLineIds, setDnsLineIds] = useState<Set<string>>(new Set());
    const [dnsSaving, setDnsSaving] = useState(false);
    const [newAddress, setNewAddress] = useState('');
    const [addressAdding, setAddressAdding] = useState(false);
    const [editingAddressId, setEditingAddressId] = useState('');
    const [editingAddress, setEditingAddress] = useState('');
    const [addressBusyId, setAddressBusyId] = useState('');
    const [cache, setCache] = useState<NodeCacheConfig | null>(null);
    const [cacheSaving, setCacheSaving] = useState(false);
    const [cacheMessage, setCacheMessage] = useState('');

    const [sshIp, setSshIp] = useState('');
    const [sshPort, setSshPort] = useState('22');
    const [sshUser, setSshUser] = useState('root');
    const [authMethod, setAuthMethod] = useState<SSHAuthMethod>('password');
    const [password, setPassword] = useState('');
    const [privateKey, setPrivateKey] = useState('');
    const [passphrase, setPassphrase] = useState('');
    const [testing, setTesting] = useState(false);
    const [reinstalling, setReinstalling] = useState(false);
    const [testedFingerprint, setTestedFingerprint] = useState('');
    const [testAttemptFingerprint, setTestAttemptFingerprint] = useState('');
    const [testMessage, setTestMessage] = useState('');
    const [testError, setTestError] = useState('');

    const applyNode = useCallback((value: Node) => {
        setNode(value);
        setDnsLineIds(new Set((value.dnsLines || []).map((line) => line.dnsLineId)));
        setCache(value.cacheConfig ?? null);
        setSshIp((current) => current || value.addresses[0]?.address || '');
    }, []);

    const load = useCallback(async () => {
        if (!clusterId || !nodeId) return;
        setLoading(true);
        try {
            const [nodeData, lineData, groupData, regionData] = await Promise.all([
                api.get(nodeId),
                cluster.dnsLines(),
                cluster.groups(),
                cluster.regions(),
            ]);
            applyNode(nodeData);
            setDnsLines(lineData);
            setGroups(groupData);
            setRegions(regionData);
            setError('');
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load node');
        } finally {
            setLoading(false);
        }
    }, [api, applyNode, cluster, clusterId, nodeId]);

    const refreshNode = useCallback(async () => {
        if (!nodeId) return;
        const value = await api.get(nodeId);
        applyNode(value);
    }, [api, applyNode, nodeId]);

    useEffect(() => {
        void load();
    }, [load]);

    const dnsOptions = useMemo(
        () =>
            dnsLines.map((line) => ({
                id: line.id,
                name: line.name,
                detail: line.providerCode,
            })),
        [dnsLines]
    );
    const groupNames = useMemo(
        () =>
            (node?.groupMemberships || []).map(
                (membership) =>
                    groups.find((group) => group.id === membership.groupId)?.name ||
                    membership.groupId
            ),
        [groups, node?.groupMemberships]
    );
    const regionNames = useMemo(
        () =>
            (node?.regionMemberships || []).map(
                (membership) =>
                    regions.find((region) => region.id === membership.regionId)?.name ||
                    membership.regionId
            ),
        [node?.regionMemberships, regions]
    );

    const saveDNSLines = async () => {
        if (!node) return;
        setDnsSaving(true);
        setError('');
        try {
            await api.updateDNSLines(node.id, Array.from(dnsLineIds));
            await refreshNode();
        } catch (saveError) {
            setError(
                saveError instanceof ApiError ? saveError.message : 'Failed to update DNS lines'
            );
        } finally {
            setDnsSaving(false);
        }
    };

    const addAddress = async () => {
        if (!node || !newAddress.trim()) return;
        setAddressAdding(true);
        setError('');
        try {
            await api.addAddress(node.id, {
                address: newAddress.trim(),
            });
            setNewAddress('');
            await refreshNode();
        } catch (addressError) {
            setError(
                addressError instanceof ApiError ? addressError.message : 'Failed to add address'
            );
        } finally {
            setAddressAdding(false);
        }
    };

    const saveAddress = async (addressId: string) => {
        if (!node || !editingAddress.trim()) return;
        setAddressBusyId(addressId);
        setError('');
        try {
            await api.updateAddress(node.id, addressId, { address: editingAddress.trim() });
            setEditingAddressId('');
            setEditingAddress('');
            await refreshNode();
        } catch (addressError) {
            setError(
                addressError instanceof ApiError ? addressError.message : 'Failed to update address'
            );
        } finally {
            setAddressBusyId('');
        }
    };

    const removeAddress = async (addressId: string, address: string) => {
        if (!node || !window.confirm(`Delete IP address ${address}?`)) return;
        setAddressBusyId(addressId);
        setError('');
        try {
            await api.deleteAddress(node.id, addressId);
            await refreshNode();
        } catch (addressError) {
            setError(
                addressError instanceof ApiError ? addressError.message : 'Failed to delete address'
            );
        } finally {
            setAddressBusyId('');
        }
    };

    const saveCache = async () => {
        if (!node || !cache) return;
        setCacheSaving(true);
        setCacheMessage('');
        setError('');
        try {
            const result = await api.updateCacheConfig(node.id, cache);
            setCache(result.cache_config);
            setCacheMessage(
                result.synced
                    ? 'Configuration saved and synchronized to the node.'
                    : result.sync_error
                      ? `Saved, but synchronization failed: ${result.sync_error}`
                      : 'Configuration saved. The node is currently unavailable for synchronization.'
            );
        } catch (cacheError) {
            setError(
                cacheError instanceof ApiError ? cacheError.message : 'Failed to update cache'
            );
        } finally {
            setCacheSaving(false);
        }
    };

    const ssh = useMemo<NodeSSH>(
        () => ({
            entry_ip: sshIp.trim(),
            port: Number(sshPort) || 22,
            user: sshUser.trim(),
            password: authMethod === 'password' ? password || undefined : undefined,
            private_key: authMethod === 'private_key' ? privateKey || undefined : undefined,
            passphrase: authMethod === 'private_key' ? passphrase || undefined : undefined,
        }),
        [authMethod, passphrase, password, privateKey, sshIp, sshPort, sshUser]
    );
    const fingerprint = useMemo(() => JSON.stringify(ssh), [ssh]);
    const fingerprintRef = useRef(fingerprint);
    fingerprintRef.current = fingerprint;
    const connectionVerified = testedFingerprint === fingerprint;
    const canTestConnection = Boolean(
        sshIp.trim() && sshUser.trim() && (authMethod === 'password' ? password : privateKey)
    );

    const testConnection = async () => {
        const tested = fingerprint;
        setTesting(true);
        setTestedFingerprint('');
        setTestAttemptFingerprint(tested);
        setTestMessage('');
        setTestError('');
        try {
            const result = await api.testConnection(ssh);
            if (fingerprintRef.current !== tested) return;
            setTestedFingerprint(tested);
            setTestMessage(`Connected successfully · ${result.architecture}`);
        } catch (testConnectionError) {
            setTestError(
                testConnectionError instanceof ApiError
                    ? testConnectionError.message
                    : 'Failed to test SSH connection'
            );
        } finally {
            setTesting(false);
        }
    };

    const reinstall = async () => {
        if (!node || !connectionVerified) return;
        if (!confirm(`Reinstall the agent on "${node.name}"?`)) return;
        setReinstalling(true);
        setError('');
        try {
            await api.reinstall(
                node.id,
                ssh,
                node.status === 'PENDING' || node.status === 'INSTALLING'
            );
            setTestedFingerprint('');
            setPassword('');
            setPrivateKey('');
            setPassphrase('');
            setTab('overview');
            await refreshNode();
        } catch (reinstallError) {
            setError(
                reinstallError instanceof ApiError
                    ? reinstallError.message
                    : 'Failed to reinstall node'
            );
        } finally {
            setReinstalling(false);
        }
    };

    if (!clusterId) return <FormError message='Select a cluster to view this node.' />;

    const addressSummary = node?.addresses.length
        ? node.addresses.map((item) => item.address).join(', ')
        : '—';
    const cacheIsValid = Boolean(
        cache?.cache_directory.trim().startsWith('/') &&
            cache.max_disk_usage_percent >= 1 &&
            cache.max_disk_usage_percent <= 95 &&
            (cache.auto_max_size || cache.max_size_bytes > 0)
    );

    return (
        <div className='space-y-6'>
            <PageHeader
                actions={
                    <Button variant='ghost' onPress={() => navigate('/nodes')}>
                        <ArrowLeft className='mr-1.5 h-4 w-4' />
                        Back to nodes
                    </Button>
                }
                subtitle='Configuration, routing, storage, and installation management.'
                title={node?.name || 'Node details'}
            />

            {error && <FormError message={error} />}
            {loading && (
                <ContentCard className='p-10 text-center text-sm text-muted'>
                    Loading node…
                </ContentCard>
            )}

            {!loading && node && (
                <>
                    {node.installError && (
                        <ContentCard className='overflow-hidden p-0' noPadding>
                            <div className='border-t border-danger/20 bg-danger/10 px-5 py-3 text-sm text-danger'>
                                <div className='font-medium'>Installation error</div>
                                <pre className='mt-1 whitespace-pre-wrap break-words font-sans text-xs leading-5'>
                                    {node.installError}
                                </pre>
                            </div>
                        </ContentCard>
                    )}

                    <div className='flex gap-1 overflow-x-auto rounded-xl border border-border bg-surface p-1.5'>
                        {tabs.map((item) => {
                            const Icon = item.icon;
                            return (
                                <button
                                    className={`flex shrink-0 items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium transition-colors ${
                                        tab === item.id
                                            ? 'bg-surface-secondary text-foreground shadow-sm'
                                            : 'text-muted hover:text-foreground'
                                    }`}
                                    key={item.id}
                                    type='button'
                                    onClick={() => setTab(item.id)}
                                >
                                    <Icon className='h-4 w-4' />
                                    {item.label}
                                </button>
                            );
                        })}
                    </div>

                    {tab === 'overview' && (
                        <div className='space-y-6'>
                            <div className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
                                <Metric
                                    label='Status'
                                    value={<StatusBadge status={node.status} />}
                                />
                                <Metric label='IP addresses' value={addressSummary} />
                                <Metric label='DNS lines' value={dnsLineIds.size || 'Default'} />
                                <Metric
                                    label='Site configurations'
                                    value={node.siteConfigVersions?.length || 0}
                                />
                            </div>
                            <div className='grid gap-6 lg:grid-cols-2'>
                                <ContentCard className='overflow-hidden p-0' noPadding>
                                    <SectionTitle
                                        description='Placement and routing membership for this node.'
                                        icon={MapPin}
                                        title='Membership'
                                    />
                                    <div className='space-y-5 p-5'>
                                        <FormField label='Groups'>
                                            <NameChips
                                                empty='No groups assigned'
                                                names={groupNames}
                                            />
                                        </FormField>
                                        <FormField label='Regions'>
                                            <NameChips
                                                empty='No regions assigned'
                                                names={regionNames}
                                            />
                                        </FormField>
                                    </div>
                                </ContentCard>
                                <ContentCard className='overflow-hidden p-0' noPadding>
                                    <SectionTitle
                                        description='Published site configuration versions on this node.'
                                        icon={Database}
                                        title='Site configurations'
                                    />
                                    <div className='divide-y divide-border'>
                                        {(node.siteConfigVersions || []).length === 0 ? (
                                            <p className='p-5 text-sm text-muted'>
                                                No site configurations published.
                                            </p>
                                        ) : (
                                            (node.siteConfigVersions || []).map((version) => (
                                                <div
                                                    className='flex items-center justify-between gap-3 px-5 py-3'
                                                    key={version.site_id}
                                                >
                                                    <span className='truncate font-mono text-xs'>
                                                        {version.site_id}
                                                    </span>
                                                    <div className='flex items-center gap-2'>
                                                        <span className='text-xs text-muted'>
                                                            v{version.version}
                                                        </span>
                                                        {version.status && (
                                                            <StatusBadge status={version.status} />
                                                        )}
                                                    </div>
                                                </div>
                                            ))
                                        )}
                                    </div>
                                </ContentCard>
                            </div>
                        </div>
                    )}

                    {tab === 'network' && (
                        <div className='grid gap-6 xl:grid-cols-2'>
                            <ContentCard className='overflow-visible p-0' noPadding>
                                <SectionTitle
                                    description='Choose every provider routing line served by this node.'
                                    icon={Globe2}
                                    title='DNS routing lines'
                                />
                                <div className='space-y-4 p-5'>
                                    <SearchableMultiAddField
                                        addLabel='Modify DNS lines'
                                        dialogTitle='Configure DNS lines'
                                        emptyLabel='Default routing only'
                                        itemLabel='DNS line'
                                        options={dnsOptions}
                                        searchPlaceholder='Search by name or provider code…'
                                        selected={dnsLineIds}
                                        onChange={setDnsLineIds}
                                    />
                                    <div className='flex justify-end border-t border-border pt-4'>
                                        <Button
                                            isDisabled={dnsSaving}
                                            onPress={() => void saveDNSLines()}
                                        >
                                            <Save className='mr-1.5 h-4 w-4' />
                                            {dnsSaving ? 'Saving…' : 'Save DNS configuration'}
                                        </Button>
                                    </div>
                                </div>
                            </ContentCard>

                            <ContentCard className='overflow-hidden p-0' noPadding>
                                <SectionTitle
                                    description='IP addresses used for traffic and agent communication.'
                                    icon={Network}
                                    title='Network addresses'
                                />
                                <div className='divide-y divide-border'>
                                    {node.addresses.map((address) => (
                                        <div
                                            className='flex items-center justify-between gap-3 px-5 py-3'
                                            key={address.id}
                                        >
                                            {editingAddressId === address.id ? (
                                                <Input
                                                    autoFocus
                                                    aria-label='IP address'
                                                    className='max-w-md flex-1 font-mono'
                                                    value={editingAddress}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        setEditingAddress(event.target.value)
                                                    }
                                                />
                                            ) : (
                                                <span className='font-mono text-sm'>
                                                    {address.address}
                                                </span>
                                            )}
                                            <div className='flex shrink-0 items-center gap-1'>
                                                {editingAddressId === address.id ? (
                                                    <>
                                                        <Button
                                                            isIconOnly
                                                            aria-label='Save IP address'
                                                            isDisabled={
                                                                !editingAddress.trim() ||
                                                                addressBusyId === address.id
                                                            }
                                                            size='sm'
                                                            onPress={() =>
                                                                void saveAddress(address.id)
                                                            }
                                                        >
                                                            <Check className='h-4 w-4' />
                                                        </Button>
                                                        <Button
                                                            isIconOnly
                                                            aria-label='Cancel editing'
                                                            size='sm'
                                                            variant='ghost'
                                                            onPress={() => setEditingAddressId('')}
                                                        >
                                                            <X className='h-4 w-4' />
                                                        </Button>
                                                    </>
                                                ) : (
                                                    <>
                                                        <Button
                                                            isIconOnly
                                                            aria-label='Edit IP address'
                                                            size='sm'
                                                            variant='ghost'
                                                            onPress={() => {
                                                                setEditingAddressId(address.id);
                                                                setEditingAddress(address.address);
                                                            }}
                                                        >
                                                            <Pencil className='h-4 w-4' />
                                                        </Button>
                                                        <Button
                                                            isIconOnly
                                                            aria-label='Delete IP address'
                                                            isDisabled={
                                                                addressBusyId === address.id
                                                            }
                                                            size='sm'
                                                            variant='ghost'
                                                            onPress={() =>
                                                                void removeAddress(
                                                                    address.id,
                                                                    address.address
                                                                )
                                                            }
                                                        >
                                                            <Trash2 className='h-4 w-4 text-danger' />
                                                        </Button>
                                                    </>
                                                )}
                                            </div>
                                        </div>
                                    ))}
                                    {node.addresses.length === 0 && (
                                        <div className='px-5 py-6 text-sm text-muted'>
                                            No IP addresses. This node is excluded from DNS
                                            resolution.
                                        </div>
                                    )}
                                </div>
                                <div className='space-y-3 border-t border-border bg-surface-secondary/20 p-5'>
                                    <div className='text-sm font-medium'>Add address</div>
                                    <div className='flex flex-col gap-3 sm:flex-row sm:items-center'>
                                        <Input
                                            aria-label='New IP address'
                                            className='flex-1'
                                            placeholder='203.0.113.10'
                                            value={newAddress}
                                            variant='secondary'
                                            onChange={(event) => setNewAddress(event.target.value)}
                                        />
                                        <Button
                                            isDisabled={!newAddress.trim() || addressAdding}
                                            onPress={() => void addAddress()}
                                        >
                                            <Plus className='mr-1.5 h-4 w-4' />
                                            {addressAdding ? 'Adding…' : 'Add'}
                                        </Button>
                                    </div>
                                </div>
                            </ContentCard>
                        </div>
                    )}

                    {tab === 'cache' && cache && (
                        <ContentCard className='mx-auto max-w-4xl overflow-hidden p-0' noPadding>
                            <SectionTitle
                                description='Control the on-disk response cache and synchronize it to the edge agent.'
                                icon={HardDrive}
                                title='Disk cache configuration'
                            />
                            <div className='space-y-6 p-5'>
                                <FormField
                                    error={
                                        cache.cache_directory.trim().startsWith('/')
                                            ? undefined
                                            : 'Use an absolute path beginning with /.'
                                    }
                                    hint='Absolute directory on the node used to store cached responses.'
                                    htmlFor='node-cache-directory'
                                    label='Cache directory'
                                >
                                    <Input
                                        id='node-cache-directory'
                                        value={cache.cache_directory}
                                        variant='secondary'
                                        onChange={(event) =>
                                            setCache({
                                                ...cache,
                                                cache_directory: event.target.value,
                                            })
                                        }
                                    />
                                </FormField>
                                <div className='flex items-start justify-between gap-4 rounded-xl border border-border bg-surface-secondary/30 p-4'>
                                    <div>
                                        <div className='text-sm font-medium'>
                                            Automatic maximum size
                                        </div>
                                        <p className='mt-1 text-xs leading-5 text-muted'>
                                            Let the agent calculate a safe cache limit from
                                            available disk space.
                                        </p>
                                    </div>
                                    <Switch
                                        isSelected={cache.auto_max_size}
                                        onChange={(checked) =>
                                            setCache({ ...cache, auto_max_size: checked })
                                        }
                                    />
                                </div>
                                <div className='grid gap-5 sm:grid-cols-2'>
                                    <FormField
                                        error={
                                            !cache.auto_max_size && cache.max_size_bytes <= 0
                                                ? 'Enter a value greater than zero.'
                                                : undefined
                                        }
                                        hint='Required when automatic sizing is disabled.'
                                        htmlFor='node-cache-max-size'
                                        label='Maximum size (bytes)'
                                    >
                                        <Input
                                            id='node-cache-max-size'
                                            disabled={cache.auto_max_size}
                                            min={1}
                                            type='number'
                                            value={String(cache.max_size_bytes)}
                                            variant='secondary'
                                            onChange={(event) =>
                                                setCache({
                                                    ...cache,
                                                    max_size_bytes: Number(event.target.value),
                                                })
                                            }
                                        />
                                    </FormField>
                                    <FormField
                                        error={
                                            cache.max_disk_usage_percent < 1 ||
                                            cache.max_disk_usage_percent > 95
                                                ? 'Enter a percentage from 1 to 95.'
                                                : undefined
                                        }
                                        hint='The agent stops growing the cache before this threshold.'
                                        htmlFor='node-cache-disk-percent'
                                        label='Maximum disk usage (%)'
                                    >
                                        <Input
                                            id='node-cache-disk-percent'
                                            max={95}
                                            min={1}
                                            type='number'
                                            value={String(cache.max_disk_usage_percent)}
                                            variant='secondary'
                                            onChange={(event) =>
                                                setCache({
                                                    ...cache,
                                                    max_disk_usage_percent: Number(
                                                        event.target.value
                                                    ),
                                                })
                                            }
                                        />
                                    </FormField>
                                </div>
                                {cacheMessage && (
                                    <div className='rounded-xl border border-border bg-surface-secondary/40 px-4 py-3 text-sm'>
                                        {cacheMessage}
                                    </div>
                                )}
                                <div className='flex justify-end border-t border-border pt-4'>
                                    <Button
                                        isDisabled={cacheSaving || !cacheIsValid}
                                        onPress={() => void saveCache()}
                                    >
                                        <Save className='mr-1.5 h-4 w-4' />
                                        {cacheSaving ? 'Saving…' : 'Save and synchronize'}
                                    </Button>
                                </div>
                            </div>
                        </ContentCard>
                    )}

                    {tab === 'reinstall' && (
                        <div className='mx-auto max-w-5xl space-y-4'>
                            <ContentCard className='overflow-visible p-0' noPadding>
                                <div className='flex items-center gap-3 border-b border-border bg-surface-secondary/30 px-6 py-3'>
                                    <span className='flex h-6 w-6 items-center justify-center rounded-full bg-primary text-primary-foreground'>
                                        <RefreshCw className='h-3.5 w-3.5' />
                                    </span>
                                    <div>
                                        <div className='text-sm font-semibold'>SSH access</div>
                                        <div className='text-xs text-muted'>
                                            Verify temporary credentials before reinstalling the
                                            edge agent.
                                        </div>
                                    </div>
                                </div>

                                <div className='px-6 py-2'>
                                    <FormRow
                                        hint='For example, 192.168.1.100. Used to reinstall the edge agent remotely.'
                                        htmlFor='reinstall-ssh-ip'
                                        label='SSH host'
                                        required
                                    >
                                        <Input
                                            className='w-full'
                                            id='reinstall-ssh-ip'
                                            required
                                            value={sshIp}
                                            variant='secondary'
                                            onChange={(event) => setSshIp(event.target.value)}
                                        />
                                    </FormRow>
                                    <FormRow htmlFor='reinstall-ssh-port' label='SSH port' required>
                                        <Input
                                            className='w-full'
                                            id='reinstall-ssh-port'
                                            required
                                            type='number'
                                            value={sshPort}
                                            variant='secondary'
                                            onChange={(event) => setSshPort(event.target.value)}
                                        />
                                    </FormRow>
                                    <FormRow htmlFor='reinstall-ssh-user' label='SSH user' required>
                                        <Input
                                            className='w-full'
                                            id='reinstall-ssh-user'
                                            required
                                            value={sshUser}
                                            variant='secondary'
                                            onChange={(event) => setSshUser(event.target.value)}
                                        />
                                    </FormRow>
                                    <FormRow label='Authentication method' required>
                                        <div className='grid gap-3 sm:grid-cols-2'>
                                            <button
                                                className={`flex cursor-pointer items-start gap-3 rounded-xl border p-4 text-left transition-colors ${
                                                    authMethod === 'password'
                                                        ? 'border-accent bg-accent/10'
                                                        : 'border-border bg-surface hover:bg-surface-secondary'
                                                }`}
                                                type='button'
                                                onClick={() => {
                                                    setAuthMethod('password');
                                                    setPrivateKey('');
                                                    setPassphrase('');
                                                }}
                                            >
                                                <LockKeyhole className='mt-0.5 h-5 w-5 shrink-0 text-muted' />
                                                <span>
                                                    <span className='block text-sm font-semibold'>
                                                        Password
                                                    </span>
                                                    <span className='mt-1 block text-xs leading-5 text-muted'>
                                                        Authenticate with the SSH account password.
                                                    </span>
                                                </span>
                                            </button>
                                            <button
                                                className={`flex cursor-pointer items-start gap-3 rounded-xl border p-4 text-left transition-colors ${
                                                    authMethod === 'private_key'
                                                        ? 'border-accent bg-accent/10'
                                                        : 'border-border bg-surface hover:bg-surface-secondary'
                                                }`}
                                                type='button'
                                                onClick={() => {
                                                    setAuthMethod('private_key');
                                                    setPassword('');
                                                }}
                                            >
                                                <KeyRound className='mt-0.5 h-5 w-5 shrink-0 text-muted' />
                                                <span>
                                                    <span className='block text-sm font-semibold'>
                                                        Private key
                                                    </span>
                                                    <span className='mt-1 block text-xs leading-5 text-muted'>
                                                        Authenticate with a PEM-encoded private key.
                                                    </span>
                                                </span>
                                            </button>
                                        </div>
                                    </FormRow>

                                    {authMethod === 'password' ? (
                                        <FormRow
                                            htmlFor='reinstall-ssh-password'
                                            label='Password'
                                            required
                                        >
                                            <Input
                                                className='w-full'
                                                id='reinstall-ssh-password'
                                                required
                                                type='password'
                                                value={password}
                                                variant='secondary'
                                                onChange={(event) =>
                                                    setPassword(event.target.value)
                                                }
                                            />
                                        </FormRow>
                                    ) : (
                                        <>
                                            <FormRow
                                                hint='Paste the complete PEM key, including the BEGIN and END lines.'
                                                htmlFor='reinstall-ssh-key'
                                                label='Private key PEM'
                                                required
                                            >
                                                <TextArea
                                                    className='w-full font-mono text-xs'
                                                    id='reinstall-ssh-key'
                                                    required
                                                    rows={8}
                                                    spellCheck={false}
                                                    value={privateKey}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        setPrivateKey(event.target.value)
                                                    }
                                                />
                                            </FormRow>
                                            <FormRow
                                                htmlFor='reinstall-ssh-passphrase'
                                                label='Private key passphrase'
                                            >
                                                <Input
                                                    className='w-full'
                                                    id='reinstall-ssh-passphrase'
                                                    type='password'
                                                    value={passphrase}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        setPassphrase(event.target.value)
                                                    }
                                                />
                                            </FormRow>
                                        </>
                                    )}

                                    <div className='border-t border-border py-4'>
                                        <div className='flex flex-wrap items-center gap-3'>
                                            <Button
                                                isDisabled={!canTestConnection || testing}
                                                type='button'
                                                variant='secondary'
                                                onPress={() => void testConnection()}
                                            >
                                                {testing
                                                    ? 'Testing connection…'
                                                    : 'Test SSH connection'}
                                            </Button>
                                            {connectionVerified && (
                                                <span className='inline-flex items-center gap-1.5 text-sm text-success'>
                                                    <Check className='h-4 w-4' />
                                                    {testMessage}
                                                </span>
                                            )}
                                        </div>
                                        {testAttemptFingerprint === fingerprint && testError && (
                                            <div className='mt-3'>
                                                <FormError message={testError} />
                                            </div>
                                        )}
                                        {testedFingerprint && !connectionVerified && (
                                            <p className='mt-2 text-sm text-warning'>
                                                SSH settings changed. Test the connection again
                                                before reinstalling the node.
                                            </p>
                                        )}
                                    </div>
                                </div>
                            </ContentCard>

                            <div className='flex items-center justify-end gap-2'>
                                <Button
                                    type='button'
                                    variant='ghost'
                                    onPress={() => setTab('overview')}
                                >
                                    Cancel
                                </Button>
                                <Button
                                    isDisabled={!connectionVerified || reinstalling}
                                    type='button'
                                    onPress={() => void reinstall()}
                                >
                                    {reinstalling ? 'Reinstalling…' : 'Reinstall node'}
                                </Button>
                            </div>
                        </div>
                    )}
                </>
            )}
        </div>
    );
}
