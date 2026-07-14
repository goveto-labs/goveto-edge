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
    Search,
    Server,
    Trash2,
    Users,
    X,
} from 'lucide-react';
import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, clusterApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
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

function SearchableMultiAddField({
    options,
    selected,
    addLabel,
    dialogTitle,
    itemLabel,
    searchPlaceholder,
    createOption,
    emptyLabel,
    onChange,
}: {
    options: MultiAddOption[];
    selected: Set<string>;
    addLabel: string;
    dialogTitle: string;
    itemLabel: string;
    searchPlaceholder: string;
    createOption?: (name: string) => Promise<MultiAddOption>;
    emptyLabel?: string;
    onChange: (value: Set<string>) => void;
}) {
    const rowHeight = 56;
    const viewportHeight = 336;
    const overscan = 5;
    const [open, setOpen] = useState(false);
    const [draft, setDraft] = useState<Set<string>>(new Set());
    const [query, setQuery] = useState('');
    const [selectedOnly, setSelectedOnly] = useState(false);
    const [scrollTop, setScrollTop] = useState(0);
    const [newOptionName, setNewOptionName] = useState('');
    const [creating, setCreating] = useState(false);
    const [createError, setCreateError] = useState('');
    const deferredQuery = useDeferredValue(query.trim().toLocaleLowerCase());
    const scrollRef = useRef<HTMLDivElement>(null);
    const scrollRafRef = useRef<number | undefined>(undefined);

    const optionsById = useMemo(
        () => new Map(options.map((option) => [option.id, option])),
        [options]
    );
    const selectedOptions = useMemo(
        () =>
            Array.from(selected, (id) => optionsById.get(id)).filter(
                (option): option is MultiAddOption => Boolean(option)
            ),
        [optionsById, selected]
    );
    const filteredOptions = useMemo(() => {
        return options.filter((option) => {
            if (selectedOnly && !draft.has(option.id)) return false;
            if (!deferredQuery) return true;
            return `${option.name} ${option.detail ?? ''}`
                .toLocaleLowerCase()
                .includes(deferredQuery);
        });
    }, [deferredQuery, draft, options, selectedOnly]);

    const startIndex = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
    const endIndex = Math.min(
        filteredOptions.length,
        Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan
    );
    const visibleOptions = filteredOptions.slice(startIndex, endIndex);

    const resetScroll = () => {
        setScrollTop(0);
        if (scrollRef.current) scrollRef.current.scrollTop = 0;
    };

    const handleOpenChange = (nextOpen: boolean) => {
        setOpen(nextOpen);
        if (nextOpen) {
            setDraft(new Set(selected));
            setQuery('');
            setSelectedOnly(false);
            setNewOptionName('');
            setCreateError('');
            resetScroll();
        }
    };

    const toggleDraft = (id: string) => {
        setDraft((current) => {
            const next = new Set(current);
            if (next.has(id)) next.delete(id);
            else next.add(id);
            return next;
        });
    };

    const handleCreate = async () => {
        const name = newOptionName.trim();
        if (!name || !createOption || creating) return;

        const existing = options.find(
            (option) => option.name.toLocaleLowerCase() === name.toLocaleLowerCase()
        );
        if (existing) {
            setDraft((current) => new Set(current).add(existing.id));
            setNewOptionName('');
            setCreateError('');
            return;
        }

        setCreating(true);
        setCreateError('');
        try {
            const created = await createOption(name);
            setDraft((current) => new Set(current).add(created.id));
            setNewOptionName('');
            setQuery('');
            setSelectedOnly(false);
            resetScroll();
        } catch (error) {
            setCreateError(
                error instanceof Error ? error.message : `Failed to create ${itemLabel}`
            );
        } finally {
            setCreating(false);
        }
    };

    useEffect(
        () => () => {
            cancelAnimationFrame(scrollRafRef.current ?? 0);
        },
        []
    );

    const displayedSelections = selectedOptions.slice(0, 6);
    const hiddenSelectionCount = selectedOptions.length - displayedSelections.length;

    return (
        <div className='space-y-3'>
            <div className='flex min-h-8 flex-wrap items-center gap-2'>
                {selectedOptions.length === 0 && emptyLabel && (
                    <span className='text-sm text-muted'>{emptyLabel}</span>
                )}
                {displayedSelections.map((option) => (
                    <span
                        className='inline-flex items-center gap-1.5 rounded-full border border-border bg-surface-secondary px-3 py-1 text-sm'
                        key={option.id}
                    >
                        {option.name}
                        <button
                            aria-label={`Remove ${option.name}`}
                            className='rounded-full text-muted transition-colors hover:text-foreground'
                            type='button'
                            onClick={() => {
                                const next = new Set(selected);
                                next.delete(option.id);
                                onChange(next);
                            }}
                        >
                            <X className='h-3.5 w-3.5' />
                        </button>
                    </span>
                ))}
                {hiddenSelectionCount > 0 && (
                    <span className='rounded-full bg-surface-secondary px-3 py-1 text-sm text-muted'>
                        +{hiddenSelectionCount} more
                    </span>
                )}
                <Button
                    size='sm'
                    type='button'
                    variant='secondary'
                    onPress={() => handleOpenChange(true)}
                >
                    <Plus className='mr-1.5 h-4 w-4' />
                    {addLabel}
                    {selected.size > 0 && (
                        <span className='ml-1 rounded-full bg-primary/10 px-1.5 text-xs text-primary'>
                            {selected.size}
                        </span>
                    )}
                </Button>
            </div>

            <DialogShell
                isOpen={open}
                size='lg'
                subtitle={`${options.length} available. Search and select the ${itemLabel}s assigned to this node.`}
                title={dialogTitle}
                onOpenChange={handleOpenChange}
            >
                <div className='space-y-4 px-6 py-5'>
                    {createOption && (
                        <div className='rounded-xl border border-border bg-surface-secondary/30 p-3'>
                            <div className='mb-2 text-sm font-medium'>Create {itemLabel}</div>
                            <div className='flex gap-2'>
                                <Input
                                    aria-label={`New ${itemLabel} name`}
                                    placeholder={`${itemLabel} name`}
                                    value={newOptionName}
                                    variant='secondary'
                                    onChange={(event) => {
                                        setNewOptionName(event.target.value);
                                        setCreateError('');
                                    }}
                                    onKeyDown={(event) => {
                                        if (event.key === 'Enter') {
                                            event.preventDefault();
                                            void handleCreate();
                                        }
                                    }}
                                />
                                <Button
                                    isDisabled={!newOptionName.trim() || creating}
                                    type='button'
                                    onPress={() => void handleCreate()}
                                >
                                    <Plus className='mr-1.5 h-4 w-4' />
                                    {creating ? 'Creating…' : 'Create'}
                                </Button>
                            </div>
                            {createError && (
                                <p className='mt-2 text-sm text-danger'>{createError}</p>
                            )}
                        </div>
                    )}
                    <div className='flex flex-col gap-2 sm:flex-row'>
                        <div className='relative min-w-0 flex-1'>
                            <Search className='pointer-events-none absolute left-3 top-1/2 z-10 h-4 w-4 -translate-y-1/2 text-muted' />
                            <Input
                                autoFocus
                                aria-label={`Search ${itemLabel}s`}
                                className='pl-9'
                                placeholder={searchPlaceholder}
                                value={query}
                                variant='secondary'
                                onChange={(event) => {
                                    setQuery(event.target.value);
                                    resetScroll();
                                }}
                            />
                        </div>
                        <Button
                            type='button'
                            variant={selectedOnly ? 'primary' : 'secondary'}
                            onPress={() => {
                                setSelectedOnly((value) => !value);
                                resetScroll();
                            }}
                        >
                            Selected only ({draft.size})
                        </Button>
                    </div>

                    <div className='flex flex-wrap items-center justify-between gap-2 text-sm'>
                        <span className='text-muted'>
                            {filteredOptions.length} result{filteredOptions.length === 1 ? '' : 's'}
                        </span>
                        <div className='flex gap-2'>
                            <Button
                                isDisabled={filteredOptions.length === 0}
                                size='sm'
                                type='button'
                                variant='ghost'
                                onPress={() =>
                                    setDraft((current) => {
                                        const next = new Set(current);
                                        for (const option of filteredOptions) next.add(option.id);
                                        return next;
                                    })
                                }
                            >
                                Select results
                            </Button>
                            <Button
                                isDisabled={draft.size === 0}
                                size='sm'
                                type='button'
                                variant='ghost'
                                onPress={() => setDraft(new Set())}
                            >
                                Clear
                            </Button>
                        </div>
                    </div>

                    <div
                        ref={scrollRef}
                        className='overflow-y-auto rounded-xl border border-border bg-surface-secondary/20'
                        style={{ height: viewportHeight }}
                        onScroll={(event) => {
                            const nextScrollTop = event.currentTarget.scrollTop;
                            cancelAnimationFrame(scrollRafRef.current ?? 0);
                            scrollRafRef.current = requestAnimationFrame(() => {
                                setScrollTop(nextScrollTop);
                            });
                        }}
                    >
                        {filteredOptions.length === 0 ? (
                            <div className='flex h-full items-center justify-center px-6 text-center text-sm text-muted'>
                                {options.length === 0
                                    ? `No ${itemLabel}s are configured.`
                                    : `No ${itemLabel}s match the current filter.`}
                            </div>
                        ) : (
                            <div
                                className='relative'
                                style={{ height: filteredOptions.length * rowHeight }}
                            >
                                {visibleOptions.map((option, visibleIndex) => {
                                    const index = startIndex + visibleIndex;
                                    const active = draft.has(option.id);
                                    return (
                                        <button
                                            aria-pressed={active}
                                            className={`absolute inset-x-0 flex items-center gap-3 border-b border-border px-4 text-left transition-colors hover:bg-surface-secondary ${
                                                active ? 'bg-accent/10' : 'bg-surface'
                                            }`}
                                            key={option.id}
                                            style={{
                                                height: rowHeight,
                                                transform: `translateY(${index * rowHeight}px)`,
                                            }}
                                            type='button'
                                            onClick={() => toggleDraft(option.id)}
                                        >
                                            <span
                                                className={`flex h-5 w-5 shrink-0 items-center justify-center rounded border ${
                                                    active
                                                        ? 'border-primary bg-primary text-primary-foreground'
                                                        : 'border-border bg-surface'
                                                }`}
                                            >
                                                {active && <Check className='h-3.5 w-3.5' />}
                                            </span>
                                            <span className='min-w-0 flex-1'>
                                                <span className='block truncate text-sm font-medium'>
                                                    {option.name}
                                                </span>
                                                {option.detail && (
                                                    <span className='block truncate text-xs text-muted'>
                                                        {option.detail}
                                                    </span>
                                                )}
                                            </span>
                                            {active && (
                                                <span className='text-xs font-medium text-primary'>
                                                    Selected
                                                </span>
                                            )}
                                        </button>
                                    );
                                })}
                            </div>
                        )}
                    </div>
                </div>
                <DialogFooter>
                    <div className='mr-auto self-center text-sm text-muted'>
                        {draft.size} selected
                    </div>
                    <Button type='button' variant='ghost' onPress={() => setOpen(false)}>
                        Cancel
                    </Button>
                    <Button
                        type='button'
                        onPress={() => {
                            onChange(new Set(draft));
                            setOpen(false);
                        }}
                    >
                        Apply selection
                    </Button>
                </DialogFooter>
            </DialogShell>
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
    const dnsLineOptions = useMemo(
        () =>
            dnsLines.map((line) => ({
                id: line.id,
                name: line.name,
                detail: line.providerCode,
            })),
        [dnsLines]
    );
    const groupOptions = useMemo(
        () => groups.map((group) => ({ id: group.id, name: group.name })),
        [groups]
    );
    const regionOptions = useMemo(
        () => regions.map((region) => ({ id: region.id, name: region.name })),
        [regions]
    );

    const createGroupOption = useCallback(
        async (groupName: string) => {
            const created = await cluster.createGroup(groupName);
            setGroups((current) =>
                [...current.filter((group) => group.id !== created.id), created].sort((a, b) =>
                    a.name.localeCompare(b.name)
                )
            );
            return { id: created.id, name: created.name };
        },
        [cluster]
    );

    const createRegionOption = useCallback(
        async (regionName: string) => {
            const created = await cluster.createRegion(regionName);
            setRegions((current) =>
                [...current.filter((region) => region.id !== created.id), created].sort((a, b) =>
                    a.name.localeCompare(b.name)
                )
            );
            return { id: created.id, name: created.name };
        },
        [cluster]
    );

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
                                        <SearchableMultiAddField
                                            addLabel='Add DNS line'
                                            dialogTitle='Add DNS lines'
                                            itemLabel='DNS line'
                                            options={dnsLineOptions}
                                            searchPlaceholder='Search by name or provider code…'
                                            selected={dnsLineIds}
                                            onChange={setDnsLineIds}
                                        />
                                    </FormRow>

                                    <FormRow
                                        hint='Groups can be used for filtering and cache topology. A node may belong to multiple groups.'
                                        label='Groups'
                                    >
                                        <SearchableMultiAddField
                                            addLabel='Add group'
                                            createOption={createGroupOption}
                                            dialogTitle='Add groups'
                                            itemLabel='group'
                                            options={groupOptions}
                                            searchPlaceholder='Search groups…'
                                            selected={groupIds}
                                            onChange={setGroupIds}
                                        />
                                    </FormRow>

                                    <FormRow
                                        hint='Regions support geographic reporting and routing. A node may belong to multiple regions.'
                                        label='Regions'
                                    >
                                        <SearchableMultiAddField
                                            addLabel='Add region'
                                            createOption={createRegionOption}
                                            dialogTitle='Add regions'
                                            itemLabel='region'
                                            options={regionOptions}
                                            searchPlaceholder='Search regions…'
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

                                        <FormRow htmlFor='node-ssh-port' label='SSH port' required>
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
