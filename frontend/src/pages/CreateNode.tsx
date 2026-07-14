import type { ClusterGroup, ClusterRegion, DNSLine } from '@/api';

import { Button, Input, TextArea } from '@heroui/react';
import {
    ArrowLeft,
    Check,
    ChevronDown,
    ChevronRight,
    KeyRound,
    LockKeyhole,
    Plus,
    Server,
    Trash2,
    Users,
    X,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, clusterApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { FormError } from '@/components/FormField.tsx';
import { FormRow } from '@/components/FormRow.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

type CreationMode = 'single' | 'batch';
type SSHAuthMethod = 'password' | 'private_key';

interface MultiAddOption {
    id: string;
    name: string;
    detail?: string;
}

function MultiAddField({
    options,
    selected,
    addLabel,
    emptyLabel,
    onChange,
}: {
    options: MultiAddOption[];
    selected: Set<string>;
    addLabel: string;
    emptyLabel: string;
    onChange: (value: Set<string>) => void;
}) {
    const [open, setOpen] = useState(false);
    const selectedOptions = options.filter((option) => selected.has(option.id));

    const toggle = (id: string) => {
        const next = new Set(selected);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        onChange(next);
    };

    return (
        <div className='space-y-3'>
            <div className='flex min-h-8 flex-wrap items-center gap-2'>
                {selectedOptions.length === 0 && (
                    <span className='text-sm text-muted'>{emptyLabel}</span>
                )}
                {selectedOptions.map((option) => (
                    <span
                        className='inline-flex items-center gap-1.5 rounded-full border border-border bg-surface-secondary px-3 py-1 text-sm'
                        key={option.id}
                    >
                        {option.name}
                        <button
                            aria-label={`Remove ${option.name}`}
                            className='rounded-full text-muted transition-colors hover:text-foreground'
                            type='button'
                            onClick={() => toggle(option.id)}
                        >
                            <X className='h-3.5 w-3.5' />
                        </button>
                    </span>
                ))}
                <Button size='sm' type='button' variant='secondary' onPress={() => setOpen(!open)}>
                    <Plus className='mr-1.5 h-4 w-4' />
                    {addLabel}
                </Button>
            </div>
            {open && (
                <div className='grid gap-2 rounded-xl border border-border bg-surface-secondary/40 p-3 sm:grid-cols-2'>
                    {options.length === 0 ? (
                        <p className='text-sm text-muted'>No options are configured.</p>
                    ) : (
                        options.map((option) => {
                            const active = selected.has(option.id);
                            return (
                                <button
                                    className={`flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-left text-sm transition-colors ${
                                        active
                                            ? 'border-accent bg-accent/10 text-foreground'
                                            : 'border-border bg-surface hover:bg-surface-secondary'
                                    }`}
                                    key={option.id}
                                    type='button'
                                    onClick={() => toggle(option.id)}
                                >
                                    <span>
                                        <span className='block font-medium'>{option.name}</span>
                                        {option.detail && (
                                            <span className='block text-xs text-muted'>
                                                {option.detail}
                                            </span>
                                        )}
                                    </span>
                                    {active && <Check className='h-4 w-4 shrink-0 text-accent' />}
                                </button>
                            );
                        })
                    )}
                </div>
            )}
        </div>
    );
}

function SectionHeader({ number, title }: { number: number; title: string }) {
    return (
        <div className='flex items-center gap-3 border-b border-border bg-surface-secondary/30 px-6 py-3'>
            <span className='flex h-6 w-6 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground'>
                {number}
            </span>
            <span className='text-sm font-semibold'>{title}</span>
        </div>
    );
}

function ModeTabs({
    mode,
    onChange,
}: {
    mode: CreationMode;
    onChange: (mode: CreationMode) => void;
}) {
    return (
        <div className='flex flex-col gap-1'>
            <button
                className={`flex items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium transition-colors cursor-pointer ${
                    mode === 'single'
                        ? 'bg-surface-secondary text-foreground'
                        : 'text-muted hover:bg-surface-secondary hover:text-foreground'
                }`}
                onClick={() => onChange('single')}
                type='button'
            >
                <Server className='h-4 w-4' />
                Single node
            </button>
            <button
                className={`flex items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium transition-colors cursor-pointer ${
                    mode === 'batch'
                        ? 'bg-surface-secondary text-foreground'
                        : 'text-muted hover:bg-surface-secondary hover:text-foreground'
                }`}
                onClick={() => onChange('batch')}
                type='button'
            >
                <Users className='h-4 w-4' />
                Batch create
            </button>
        </div>
    );
}

export default function CreateNode() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
    const nodeApi = useMemo(() => nodesApi(clusterId), [clusterId]);

    const [mode, setMode] = useState<CreationMode>('single');
    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [groups, setGroups] = useState<ClusterGroup[]>([]);
    const [regions, setRegions] = useState<ClusterRegion[]>([]);
    const [error, setError] = useState('');

    const [submitting, setSubmitting] = useState(false);
    const [submitError, setSubmitError] = useState('');
    const [name, setName] = useState('');
    const idRef = useRef(0);
    const [addresses, setAddresses] = useState<{ id: number; value: string }[]>([
        { id: idRef.current++, value: '' },
    ]);
    const [dnsLineIds, setDnsLineIds] = useState<Set<string>>(new Set());
    const [groupIds, setGroupIds] = useState<Set<string>>(new Set());
    const [regionIds, setRegionIds] = useState<Set<string>>(new Set());
    const [sshExpanded, setSshExpanded] = useState(true);
    const [sshIp, setSshIp] = useState('');
    const [sshPort, setSshPort] = useState('22');
    const [sshUser, setSshUser] = useState('');
    const [sshAuthMethod, setSshAuthMethod] = useState<SSHAuthMethod>('password');
    const [sshPassword, setSshPassword] = useState('');
    const [sshKey, setSshKey] = useState('');
    const [sshPassphrase, setSshPassphrase] = useState('');

    const loadOptions = useCallback(async () => {
        if (!clusterId) return;
        try {
            const [d, g, r] = await Promise.all([
                cluster.dnsLines(),
                cluster.groups(),
                cluster.regions(),
            ]);
            setDnsLines(d);
            setGroups(g);
            setRegions(r);
            setError('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load options');
        }
    }, [cluster, clusterId]);

    useEffect(() => {
        loadOptions();
    }, [loadOptions]);

    const handleAddAddress = () =>
        setAddresses((prev) => [...prev, { id: idRef.current++, value: '' }]);
    const handleRemoveAddress = (id: number) => {
        setAddresses((prev) => prev.filter((item) => item.id !== id));
    };
    const handleAddressChange = (id: number, value: string) => {
        setAddresses((prev) => prev.map((item) => (item.id === id ? { ...item, value } : item)));
    };

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!clusterId) return;
        setSubmitting(true);
        setSubmitError('');
        try {
            await nodeApi.create({
                name,
                addresses: addresses.map((item) => item.value).filter(Boolean),
                dns_line_ids: Array.from(dnsLineIds),
                group_ids: Array.from(groupIds),
                region_ids: Array.from(regionIds),
                ssh: {
                    entry_ip: sshIp,
                    port: Number(sshPort) || 22,
                    user: sshUser,
                    password: sshAuthMethod === 'password' ? sshPassword || undefined : undefined,
                    private_key: sshAuthMethod === 'private_key' ? sshKey || undefined : undefined,
                    passphrase:
                        sshAuthMethod === 'private_key' ? sshPassphrase || undefined : undefined,
                },
            });
            navigate('/nodes');
        } catch (err) {
            setSubmitError(err instanceof ApiError ? err.message : 'Failed to create node');
        } finally {
            setSubmitting(false);
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Add an edge node and configure SSH access.'
                    title='Create node'
                />
                <ContentCard className='p-8 text-center'>
                    <div className='text-sm text-muted'>Select a cluster to create a node.</div>
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                actions={
                    <Button variant='ghost' onPress={() => navigate('/nodes')}>
                        <ArrowLeft className='mr-1.5 h-4 w-4' />
                        Back to nodes
                    </Button>
                }
                subtitle='Add an edge node and configure SSH access for remote management.'
                title='Create node'
            />

            {error && <FormError message={error} />}

            {mode === 'batch' ? (
                <ContentCard className='p-8 text-center'>
                    <div className='space-y-3'>
                        <Users className='mx-auto h-10 w-10 text-muted' />
                        <div className='text-sm font-medium'>Batch creation is coming soon</div>
                        <p className='text-xs text-muted'>Use single-node creation for now.</p>
                        <Button size='sm' variant='primary' onPress={() => setMode('single')}>
                            Back to single node
                        </Button>
                    </div>
                </ContentCard>
            ) : (
                <form onSubmit={handleSubmit}>
                    <div className='grid grid-cols-1 gap-6 lg:grid-cols-[200px_1fr]'>
                        <div className='hidden lg:block'>
                            <ModeTabs mode={mode} onChange={setMode} />
                        </div>

                        <div className='space-y-6'>
                            <ContentCard className='overflow-visible p-0' noPadding>
                                <SectionHeader number={1} title='Node information' />

                                <div className='px-6 py-2'>
                                    {submitError && (
                                        <div className='mb-4'>
                                            <FormError message={submitError} />
                                        </div>
                                    )}

                                    <FormRow htmlFor='node-name' label='Node name' required>
                                        <Input
                                            autoFocus
                                            id='node-name'
                                            required
                                            variant='secondary'
                                            value={name}
                                            onChange={(e) => setName(e.target.value)}
                                        />
                                    </FormRow>

                                    <FormRow label='IP' required>
                                        <div className='space-y-2'>
                                            {addresses.map((item, index) => (
                                                <div
                                                    key={item.id}
                                                    className='flex items-center gap-2'
                                                >
                                                    <Input
                                                        aria-label={`IP address ${index + 1}`}
                                                        className='flex-1'
                                                        required={index === 0}
                                                        variant='secondary'
                                                        value={item.value}
                                                        onChange={(e) =>
                                                            handleAddressChange(
                                                                item.id,
                                                                e.target.value
                                                            )
                                                        }
                                                    />
                                                    {addresses.length > 1 && (
                                                        <Button
                                                            isIconOnly
                                                            aria-label='Remove address'
                                                            className='shrink-0 text-muted'
                                                            size='sm'
                                                            variant='ghost'
                                                            onPress={() =>
                                                                handleRemoveAddress(item.id)
                                                            }
                                                        >
                                                            <Trash2 className='h-4 w-4' />
                                                        </Button>
                                                    )}
                                                </div>
                                            ))}
                                            <Button
                                                className='w-fit gap-1'
                                                size='sm'
                                                variant='ghost'
                                                onPress={handleAddAddress}
                                            >
                                                <Plus className='h-4 w-4' />
                                                Add address
                                            </Button>
                                        </div>
                                    </FormRow>

                                    <FormRow
                                        hint='Select every DNS routing line served by this node.'
                                        label='DNS lines'
                                    >
                                        <MultiAddField
                                            addLabel='Add DNS line'
                                            emptyLabel='No DNS lines selected'
                                            options={dnsLines.map((line) => ({
                                                id: line.id,
                                                name: line.name,
                                                detail: line.providerCode,
                                            }))}
                                            selected={dnsLineIds}
                                            onChange={setDnsLineIds}
                                        />
                                    </FormRow>

                                    <FormRow
                                        hint='Groups can be used for filtering and cache topology. A node may belong to multiple groups.'
                                        label='Groups'
                                    >
                                        <MultiAddField
                                            addLabel='Add group'
                                            emptyLabel='No groups selected'
                                            options={groups}
                                            selected={groupIds}
                                            onChange={setGroupIds}
                                        />
                                    </FormRow>

                                    <FormRow
                                        hint='Regions support geographic reporting and routing. A node may belong to multiple regions.'
                                        label='Regions'
                                    >
                                        <MultiAddField
                                            addLabel='Add region'
                                            emptyLabel='No regions selected'
                                            options={regions}
                                            selected={regionIds}
                                            onChange={setRegionIds}
                                        />
                                    </FormRow>
                                </div>
                            </ContentCard>

                            <ContentCard className='overflow-visible p-0' noPadding>
                                <button
                                    className='flex w-full items-center justify-between border-b border-border bg-surface-secondary/30 px-6 py-3 text-left'
                                    onClick={() => setSshExpanded((v) => !v)}
                                    type='button'
                                >
                                    <div className='flex items-center gap-3'>
                                        <span className='flex h-6 w-6 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground'>
                                            2
                                        </span>
                                        <span className='text-sm font-semibold'>SSH access</span>
                                    </div>
                                    {sshExpanded ? (
                                        <ChevronDown className='h-4 w-4 text-muted' />
                                    ) : (
                                        <ChevronRight className='h-4 w-4 text-muted' />
                                    )}
                                </button>

                                {sshExpanded && (
                                    <div className='px-6 py-2'>
                                        <FormRow
                                            hint='For example, 192.168.1.100. Used to install the edge agent remotely.'
                                            htmlFor='node-ssh-ip'
                                            label='SSH host'
                                            required
                                        >
                                            <Input
                                                id='node-ssh-ip'
                                                required
                                                className='w-full'
                                                variant='secondary'
                                                value={sshIp}
                                                onChange={(e) => setSshIp(e.target.value)}
                                            />
                                        </FormRow>

                                        <FormRow
                                            hint='Usually port 22.'
                                            htmlFor='node-ssh-port'
                                            label='SSH port'
                                            required
                                        >
                                            <Input
                                                id='node-ssh-port'
                                                required
                                                type='number'
                                                className='w-full'
                                                variant='secondary'
                                                value={sshPort}
                                                onChange={(e) => setSshPort(e.target.value)}
                                            />
                                        </FormRow>

                                        <FormRow htmlFor='node-ssh-user' label='SSH user' required>
                                            <Input
                                                id='node-ssh-user'
                                                required
                                                className='w-full'
                                                variant='secondary'
                                                value={sshUser}
                                                onChange={(e) => setSshUser(e.target.value)}
                                            />
                                        </FormRow>

                                        <FormRow label='Authentication method' required>
                                            <div className='grid gap-3 sm:grid-cols-2'>
                                                <button
                                                    className={`flex cursor-pointer items-start gap-3 rounded-xl border p-4 text-left transition-colors ${
                                                        sshAuthMethod === 'password'
                                                            ? 'border-accent bg-accent/10'
                                                            : 'border-border bg-surface hover:bg-surface-secondary'
                                                    }`}
                                                    type='button'
                                                    onClick={() => {
                                                        setSshAuthMethod('password');
                                                        setSshKey('');
                                                        setSshPassphrase('');
                                                    }}
                                                >
                                                    <LockKeyhole className='mt-0.5 h-5 w-5 shrink-0 text-muted' />
                                                    <span>
                                                        <span className='block text-sm font-semibold'>
                                                            Password
                                                        </span>
                                                        <span className='mt-1 block text-xs leading-5 text-muted'>
                                                            Authenticate with the SSH account
                                                            password.
                                                        </span>
                                                    </span>
                                                </button>
                                                <button
                                                    className={`flex cursor-pointer items-start gap-3 rounded-xl border p-4 text-left transition-colors ${
                                                        sshAuthMethod === 'private_key'
                                                            ? 'border-accent bg-accent/10'
                                                            : 'border-border bg-surface hover:bg-surface-secondary'
                                                    }`}
                                                    type='button'
                                                    onClick={() => {
                                                        setSshAuthMethod('private_key');
                                                        setSshPassword('');
                                                    }}
                                                >
                                                    <KeyRound className='mt-0.5 h-5 w-5 shrink-0 text-muted' />
                                                    <span>
                                                        <span className='block text-sm font-semibold'>
                                                            Private key
                                                        </span>
                                                        <span className='mt-1 block text-xs leading-5 text-muted'>
                                                            Authenticate with a PEM-encoded private
                                                            key.
                                                        </span>
                                                    </span>
                                                </button>
                                            </div>
                                        </FormRow>

                                        {sshAuthMethod === 'password' && (
                                            <FormRow
                                                htmlFor='node-ssh-password'
                                                label='Password'
                                                required
                                            >
                                                <Input
                                                    id='node-ssh-password'
                                                    className='w-full'
                                                    required
                                                    type='password'
                                                    variant='secondary'
                                                    value={sshPassword}
                                                    onChange={(e) => setSshPassword(e.target.value)}
                                                />
                                            </FormRow>
                                        )}

                                        {sshAuthMethod === 'private_key' && (
                                            <>
                                                <FormRow
                                                    hint='Paste the complete PEM key, including the BEGIN and END lines.'
                                                    htmlFor='node-ssh-key'
                                                    label='Private key PEM'
                                                    required
                                                >
                                                    <TextArea
                                                        id='node-ssh-key'
                                                        className='w-full font-mono text-xs'
                                                        required
                                                        rows={8}
                                                        spellCheck={false}
                                                        variant='secondary'
                                                        value={sshKey}
                                                        onChange={(e) => setSshKey(e.target.value)}
                                                    />
                                                </FormRow>

                                                <FormRow
                                                    htmlFor='node-ssh-passphrase'
                                                    label='Private key passphrase'
                                                >
                                                    <Input
                                                        id='node-ssh-passphrase'
                                                        className='w-full'
                                                        type='password'
                                                        variant='secondary'
                                                        value={sshPassphrase}
                                                        onChange={(e) =>
                                                            setSshPassphrase(e.target.value)
                                                        }
                                                    />
                                                </FormRow>
                                            </>
                                        )}
                                    </div>
                                )}
                            </ContentCard>

                            <div className='flex items-center gap-2 justify-end'>
                                <Button
                                    type='button'
                                    variant='ghost'
                                    onPress={() => navigate('/nodes')}
                                >
                                    Cancel
                                </Button>
                                <Button isDisabled={submitting} type='submit' variant='primary'>
                                    {submitting ? 'Creating…' : 'Create node'}
                                </Button>
                            </div>
                        </div>
                    </div>
                </form>
            )}
        </div>
    );
}
