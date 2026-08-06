import type {
    DNSManagedRecord,
    DNSProviderDomain,
    DNSProviderType,
    DNSSyncJob,
    UpdateDNSConfig,
} from '@/api';

import { Button, Card, Input, Label, Spinner } from '@heroui/react';
import { Globe2, Pencil, Plus, RefreshCw, Save, Trash2 } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, dnsApi } from '@/api';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { canManageCluster } from '@/utils/rbac.ts';

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

export default function DNS() {
    const { clusterId, clusters } = useCluster();
    const api = useMemo(() => dnsApi(clusterId), [clusterId]);
    const activeClusterRef = useRef(clusterId);
    const loadGenerationRef = useRef(0);
    const statusGenerationRef = useRef(0);
    activeClusterRef.current = clusterId;

    const [provider, setProvider] = useState<DNSProviderType>('ALIYUN');
    const [configuredProvider, setConfiguredProvider] = useState<DNSProviderType | null>(null);
    const [hostname, setHostname] = useState('');
    const [zone, setZone] = useState('');
    const [zoneId, setZoneId] = useState('');
    const [accessKeyId, setAccessKeyId] = useState('');
    const [accessKeySecret, setAccessKeySecret] = useState('');
    const [apiToken, setApiToken] = useState('');
    const [ttl, setTtl] = useState(300);
    const [proxied, setProxied] = useState(false);
    const [enabled, setEnabled] = useState(false);
    const [credentialsConfigured, setCredentialsConfigured] = useState(false);
    const [records, setRecords] = useState<DNSManagedRecord[]>([]);
    const [jobs, setJobs] = useState<DNSSyncJob[]>([]);
    const [domainDialogOpen, setDomainDialogOpen] = useState(false);
    const [providerDomains, setProviderDomains] = useState<DNSProviderDomain[]>([]);
    const [discoveringDomains, setDiscoveringDomains] = useState(false);
    const [loadedClusterId, setLoadedClusterId] = useState('');
    const [loading, setLoading] = useState(false);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');

    const isOwner = useMemo(
        () => canManageCluster(clusters.find((cluster) => cluster.id === clusterId)?.role),
        [clusterId, clusters]
    );
    const ready = Boolean(clusterId) && loadedClusterId === clusterId;
    const configured = configuredProvider !== null;
    const credentialsReusable =
        credentialsConfigured && configuredProvider !== null && configuredProvider === provider;
    const canEdit = ready && isOwner && !loading && !busy;
    const hasActiveJobs = jobs.some((job) => job.status === 'PENDING' || job.status === 'RUNNING');

    const resetState = useCallback(() => {
        setProvider('ALIYUN');
        setConfiguredProvider(null);
        setHostname('');
        setZone('');
        setZoneId('');
        setAccessKeyId('');
        setAccessKeySecret('');
        setApiToken('');
        setTtl(300);
        setProxied(false);
        setEnabled(false);
        setCredentialsConfigured(false);
        setRecords([]);
        setJobs([]);
        setDomainDialogOpen(false);
        setProviderDomains([]);
        setDiscoveringDomains(false);
        setError('');
    }, []);

    const load = useCallback(
        async (showLoading = true) => {
            const requestedClusterId = clusterId;
            if (!requestedClusterId || activeClusterRef.current !== requestedClusterId) return;

            const generation = ++loadGenerationRef.current;
            statusGenerationRef.current += 1;
            if (showLoading) setLoading(true);
            try {
                const [config, recordData, jobData] = await Promise.all([
                    api.config(),
                    api.records(),
                    api.jobs(),
                ]);
                if (
                    activeClusterRef.current !== requestedClusterId ||
                    loadGenerationRef.current !== generation
                ) {
                    return;
                }

                setHostname(config.primary_hostname ?? '');
                if (config.provider) {
                    setProvider(config.provider.type);
                    setConfiguredProvider(config.provider.type);
                    setZone(config.provider.zone);
                    setZoneId(config.provider.zone_id ?? '');
                    setTtl(config.provider.default_ttl);
                    setProxied(config.provider.proxied);
                    setEnabled(config.provider.enabled);
                    setCredentialsConfigured(config.provider.credentials_configured);
                } else {
                    setProvider('ALIYUN');
                    setConfiguredProvider(null);
                    setZone('');
                    setZoneId('');
                    setTtl(300);
                    setProxied(false);
                    setEnabled(false);
                    setCredentialsConfigured(false);
                }
                setAccessKeyId('');
                setAccessKeySecret('');
                setApiToken('');
                setRecords(recordData);
                setJobs(jobData);
                setProviderDomains(
                    config.provider
                        ? [{ name: config.provider.zone, id: config.provider.zone_id ?? undefined }]
                        : []
                );
                setLoadedClusterId(requestedClusterId);
                setError('');
            } catch (loadError) {
                if (
                    activeClusterRef.current === requestedClusterId &&
                    loadGenerationRef.current === generation
                ) {
                    setError(errorMessage(loadError, 'Failed to load DNS configuration'));
                }
            } finally {
                if (
                    activeClusterRef.current === requestedClusterId &&
                    loadGenerationRef.current === generation
                ) {
                    setLoading(false);
                }
            }
        },
        [api, clusterId]
    );

    const refreshStatus = useCallback(async () => {
        const requestedClusterId = clusterId;
        if (!requestedClusterId || activeClusterRef.current !== requestedClusterId) return;
        const generation = ++statusGenerationRef.current;
        try {
            const [recordData, jobData] = await Promise.all([api.records(), api.jobs()]);
            if (
                activeClusterRef.current === requestedClusterId &&
                statusGenerationRef.current === generation
            ) {
                setRecords(recordData);
                setJobs(jobData);
            }
        } catch (statusError) {
            if (
                activeClusterRef.current === requestedClusterId &&
                statusGenerationRef.current === generation
            ) {
                setError(errorMessage(statusError, 'Failed to refresh DNS sync status'));
            }
        }
    }, [api, clusterId]);

    useEffect(() => {
        loadGenerationRef.current += 1;
        statusGenerationRef.current += 1;
        setLoadedClusterId('');
        setBusy(false);
        resetState();
        if (clusterId) void load();
    }, [clusterId, load, resetState]);

    useEffect(() => {
        if (!ready || !hasActiveJobs) return;
        const timer = window.setInterval(() => {
            void refreshStatus();
        }, 2000);
        return () => window.clearInterval(timer);
    }, [hasActiveJobs, ready, refreshStatus]);

    useAutoRefresh(refreshStatus, ready && !hasActiveJobs);

    const mutate = async (
        operation: () => Promise<unknown>,
        fallback: string,
        afterSuccess?: () => void,
        refresh: () => Promise<void> = () => load(false)
    ) => {
        const requestedClusterId = clusterId;
        if (!requestedClusterId || !ready) return;
        setBusy(true);
        setError('');
        try {
            await operation();
            if (activeClusterRef.current !== requestedClusterId) return;
            afterSuccess?.();
            await refresh();
        } catch (mutationError) {
            if (activeClusterRef.current === requestedClusterId) {
                setError(errorMessage(mutationError, fallback));
            }
        } finally {
            if (activeClusterRef.current === requestedClusterId) setBusy(false);
        }
    };

    const save = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!canEdit) return;
        if (!zone) return;

        let credentials: Record<string, string> | undefined;
        if (provider === 'ALIYUN') {
            if ((accessKeyId && !accessKeySecret) || (!accessKeyId && accessKeySecret)) {
                setError('Both Aliyun AccessKey fields are required when changing credentials.');
                return;
            }
            if (!credentialsReusable && (!accessKeyId || !accessKeySecret)) {
                setError('Aliyun AccessKey ID and secret are required.');
                return;
            }
            if (accessKeyId && accessKeySecret) {
                credentials = {
                    access_key_id: accessKeyId,
                    access_key_secret: accessKeySecret,
                };
            }
        } else {
            if (!credentialsReusable && !apiToken) {
                setError('Cloudflare API token is required.');
                return;
            }
            if (apiToken) credentials = { api_token: apiToken };
        }

        const payload: UpdateDNSConfig = {
            primary_hostname: hostname,
            provider,
            zone,
            credentials,
            default_ttl: ttl,
            proxied: provider === 'CLOUDFLARE' && proxied,
            enabled: true,
        };
        if (provider === 'CLOUDFLARE') payload.zone_id = zoneId;

        await mutate(
            () => api.update(payload),
            'Failed to save DNS configuration',
            () => {
                setAccessKeyId('');
                setAccessKeySecret('');
                setApiToken('');
                setDomainDialogOpen(false);
            }
        );
    };

    const changeProvider = (key: React.Key | null) => {
        if (!key) return;
        const nextProvider = String(key) as DNSProviderType;
        setProvider(nextProvider);
        setAccessKeyId('');
        setAccessKeySecret('');
        setApiToken('');
        setZone('');
        setZoneId('');
        setProviderDomains([]);
        if (nextProvider === 'CLOUDFLARE' && configuredProvider !== 'CLOUDFLARE') {
            setZoneId('');
            setProxied(false);
        }
    };

    const discoveryCredentials = (): Record<string, string> | undefined | null => {
        if (provider === 'ALIYUN') {
            if ((accessKeyId && !accessKeySecret) || (!accessKeyId && accessKeySecret)) {
                setError('Both Aliyun AccessKey fields are required when changing credentials.');
                return null;
            }
            if (!credentialsReusable && (!accessKeyId || !accessKeySecret)) {
                setError('Aliyun AccessKey ID and secret are required.');
                return null;
            }
            return accessKeyId && accessKeySecret
                ? { access_key_id: accessKeyId, access_key_secret: accessKeySecret }
                : undefined;
        }
        if (!credentialsReusable && !apiToken) {
            setError('Cloudflare API token is required.');
            return null;
        }
        return apiToken ? { api_token: apiToken } : undefined;
    };

    const discoverProviderDomains = async () => {
        const credentials = discoveryCredentials();
        if (credentials === null) return;
        setDiscoveringDomains(true);
        setError('');
        try {
            const available = await api.discoverDomains({ provider, credentials });
            setProviderDomains(available);
            if (available.length === 0) {
                setError('No domains are available for this credential.');
            }
        } catch (discoverError) {
            setProviderDomains([]);
            setError(errorMessage(discoverError, 'Failed to load provider domains'));
        } finally {
            setDiscoveringDomains(false);
        }
    };

    const selectDomain = (key: React.Key | null) => {
        if (!key) return;
        const domain = providerDomains.find((item) => item.name === String(key));
        if (!domain) return;
        const previousZone = zone;
        setZone(domain.name);
        setZoneId(domain.id ?? '');
        if (!hostname || hostname === `edge.${previousZone}`) setHostname(`edge.${domain.name}`);
    };

    const sync = async () => {
        await mutate(() => api.sync(), 'Failed to enqueue DNS sync', undefined, refreshStatus);
    };

    const refreshDomain = async () => {
        await mutate(() => api.refresh(), 'Failed to refresh CDN endpoint');
    };

    const deleteDomain = async () => {
        if (
            !confirm(`Delete the CDN endpoint configuration for "${zone}" and all managed records?`)
        )
            return;
        await mutate(() => api.delete(), 'Failed to delete CDN endpoint');
    };

    const providerLabel = (type: DNSProviderType) =>
        type === 'ALIYUN' ? 'Aliyun DNS' : 'Cloudflare';

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='Manage the cluster CDN endpoint hostname.' title='DNS' />
                <Card className='p-8 text-center text-sm text-muted'>
                    Select a cluster to manage DNS.
                </Card>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                subtitle='Publish the cluster scheduling hostname to edge node IP addresses.'
                title='DNS'
            >
                <Button isDisabled={loading || busy} variant='ghost' onPress={() => void load()}>
                    <RefreshCw className='mr-2 h-4 w-4' />
                    Refresh
                </Button>
                {isOwner && !configured && (
                    <Button isDisabled={!canEdit} onPress={() => setDomainDialogOpen(true)}>
                        <Plus className='mr-2 h-4 w-4' />
                        Configure endpoint
                    </Button>
                )}
                {configured && isOwner && (
                    <Button isDisabled={!canEdit} onPress={() => setDomainDialogOpen(true)}>
                        <Pencil className='mr-2 h-4 w-4' />
                        Edit endpoint
                    </Button>
                )}
                {configured && isOwner && (
                    <Button isDisabled={!canEdit} variant='secondary' onPress={refreshDomain}>
                        <RefreshCw className='mr-2 h-4 w-4' />
                        Refresh endpoint
                    </Button>
                )}
                <Button
                    isDisabled={!canEdit || !configured || !enabled}
                    variant='secondary'
                    onPress={sync}
                >
                    <RefreshCw className='mr-2 h-4 w-4' />
                    Sync now
                </Button>
                {configured && isOwner && (
                    <Button isDisabled={!canEdit} variant='danger' onPress={deleteDomain}>
                        <Trash2 className='mr-2 h-4 w-4' />
                        Delete endpoint
                    </Button>
                )}
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}
            {!isOwner && ready && (
                <div className='rounded-lg border border-border bg-surface-secondary px-4 py-3 text-sm text-muted'>
                    DNS settings are read-only because only the cluster owner can change provider
                    configuration or records.
                </div>
            )}
            <Card className='p-5'>
                <div className='mb-3 text-xs font-semibold tracking-wide text-muted uppercase'>
                    CDN endpoint
                </div>
                {configured ? (
                    <div className='flex flex-col justify-between gap-4 sm:flex-row sm:items-center'>
                        <div>
                            <div className='flex items-center gap-2 font-semibold'>
                                <Globe2 className='h-4 w-4' />
                                {zone}
                            </div>
                            <p className='mt-1 text-sm text-muted'>
                                {providerLabel(provider)} · {hostname} · TTL {ttl}
                            </p>
                        </div>
                        <span className='text-sm text-muted'>
                            {enabled ? 'Enabled' : 'Disabled'}
                        </span>
                    </div>
                ) : (
                    <div className='py-5 text-center'>
                        <Globe2 className='mx-auto mb-3 h-8 w-8 text-muted' />
                        <h2 className='font-semibold'>No CDN endpoint configured</h2>
                        <p className='mt-1 text-sm text-muted'>
                            Add provider credentials, then choose the DNS zone for the cluster
                            hostname.
                        </p>
                    </div>
                )}
            </Card>

            <DialogShell
                icon={<Globe2 className='h-5 w-5' />}
                isDismissable={!busy}
                isOpen={domainDialogOpen}
                size='lg'
                subtitle='Select the DNS zone that hosts the cluster primary hostname.'
                title={configured ? 'Edit CDN endpoint' : 'Configure CDN endpoint'}
                onOpenChange={setDomainDialogOpen}
            >
                <form onSubmit={save}>
                    <div className='grid max-h-[70vh] gap-4 overflow-y-auto px-6 py-5 md:grid-cols-2'>
                        <SelectField
                            label='Provider'
                            options={[
                                { id: 'ALIYUN', label: 'Aliyun DNS' },
                                { id: 'CLOUDFLARE', label: 'Cloudflare' },
                            ]}
                            variant='secondary'
                            isDisabled={!canEdit}
                            value={provider}
                            onChange={changeProvider}
                        />
                        <div className='hidden md:block' />
                        {provider === 'ALIYUN' ? (
                            <>
                                <div className='flex flex-col gap-1'>
                                    <Label htmlFor='dns-key-id'>AccessKey ID</Label>
                                    <Input
                                        variant='secondary'
                                        id='dns-key-id'
                                        disabled={!canEdit}
                                        placeholder={
                                            credentialsReusable
                                                ? 'Leave blank to use saved credential'
                                                : ''
                                        }
                                        value={accessKeyId}
                                        onChange={(event) => setAccessKeyId(event.target.value)}
                                    />
                                </div>
                                <div className='flex flex-col gap-1'>
                                    <Label htmlFor='dns-key-secret'>AccessKey secret</Label>
                                    <Input
                                        variant='secondary'
                                        id='dns-key-secret'
                                        disabled={!canEdit}
                                        placeholder={
                                            credentialsReusable
                                                ? 'Leave blank to use saved credential'
                                                : ''
                                        }
                                        type='password'
                                        value={accessKeySecret}
                                        onChange={(event) => setAccessKeySecret(event.target.value)}
                                    />
                                </div>
                            </>
                        ) : (
                            <div className='flex flex-col gap-1 md:col-span-2'>
                                <Label htmlFor='dns-token'>API token</Label>
                                <Input
                                    variant='secondary'
                                    id='dns-token'
                                    disabled={!canEdit}
                                    placeholder={
                                        credentialsReusable
                                            ? 'Leave blank to use saved credential'
                                            : ''
                                    }
                                    type='password'
                                    value={apiToken}
                                    onChange={(event) => setApiToken(event.target.value)}
                                />
                            </div>
                        )}
                        <div className='md:col-span-2'>
                            <Button
                                isDisabled={!canEdit || discoveringDomains}
                                variant='secondary'
                                onPress={() => void discoverProviderDomains()}
                            >
                                {discoveringDomains ? (
                                    <Spinner className='mr-2' size='sm' />
                                ) : (
                                    <RefreshCw className='mr-2 h-4 w-4' />
                                )}
                                Load zones
                            </Button>
                        </div>
                        <SelectField
                            label='DNS zone'
                            options={providerDomains.map((domain) => ({
                                id: domain.name,
                                label: domain.name,
                            }))}
                            placeholder='Select a DNS zone'
                            variant='secondary'
                            isDisabled={!canEdit || providerDomains.length === 0}
                            value={zone}
                            onChange={selectDomain}
                        />
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='dns-hostname'>Cluster hostname</Label>
                            <Input
                                variant='secondary'
                                id='dns-hostname'
                                disabled={!canEdit || !zone}
                                placeholder={zone ? `edge.${zone}` : 'Select a DNS zone first'}
                                required
                                value={hostname}
                                onChange={(event) => setHostname(event.target.value)}
                            />
                        </div>
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='dns-ttl'>TTL</Label>
                            <Input
                                variant='secondary'
                                id='dns-ttl'
                                disabled={!canEdit}
                                max={86400}
                                min={60}
                                required
                                type='number'
                                value={String(ttl)}
                                onChange={(event) => setTtl(Number(event.target.value))}
                            />
                        </div>
                        {provider === 'CLOUDFLARE' && (
                            <label className='flex items-center gap-2 pt-7 text-sm'>
                                <input
                                    checked={proxied}
                                    disabled={!canEdit}
                                    type='checkbox'
                                    onChange={(event) => setProxied(event.target.checked)}
                                />
                                Enable Cloudflare proxy
                            </label>
                        )}
                        {error && (
                            <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground md:col-span-2'>
                                {error}
                            </div>
                        )}
                    </div>
                    <DialogFooter>
                        <Button
                            isDisabled={busy}
                            variant='ghost'
                            onPress={() => setDomainDialogOpen(false)}
                        >
                            Cancel
                        </Button>
                        <Button isDisabled={!canEdit || !zone} type='submit'>
                            <Save className='mr-2 h-4 w-4' />
                            {busy
                                ? 'Saving…'
                                : configured
                                  ? enabled
                                      ? 'Save and sync'
                                      : 'Enable and sync'
                                  : 'Add and sync'}
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <DataTable
                aria-label='DNS records'
                empty={records.length === 0}
                emptyDescription='Managed DNS records will appear after nodes and sites are synchronized.'
                emptyTitle='No DNS records yet'
                title='Managed records'
            >
                <thead>
                    <tr>
                        <th>Hostname</th>
                        <th>Type</th>
                        <th>Value</th>
                        <th>Line</th>
                        <th>Status</th>
                        <th>Detail</th>
                    </tr>
                </thead>
                <tbody>
                    {records.map((record) => (
                        <tr key={record.id}>
                            <td className='font-mono text-xs'>{record.hostname}</td>
                            <td>{record.type}</td>
                            <td className='font-mono text-xs'>{record.value}</td>
                            <td>{record.dnsLineKey}</td>
                            <td>{record.status}</td>
                            <td>{record.lastError ?? record.lastSyncedAt ?? '—'}</td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>

            <DataTable
                aria-label='DNS jobs'
                empty={jobs.length === 0}
                emptyDescription='Jobs appear when edge node IP addresses are synchronized to DNS.'
                emptyTitle='No DNS jobs yet'
                title='Node IP synchronization jobs'
            >
                <thead>
                    <tr>
                        <th>Job</th>
                        <th>Action</th>
                        <th>Status</th>
                        <th>Attempts</th>
                        <th>Error</th>
                        <th>Created</th>
                    </tr>
                </thead>
                <tbody>
                    {jobs.map((job) => (
                        <tr key={job.id}>
                            <td className='font-mono text-xs max-w-48'>{job.id}</td>
                            <td>{job.action === 'UPSERT_CLUSTER' ? 'SYNC_NODE_IP' : job.action}</td>
                            <td>{job.status}</td>
                            <td className='max-w-12'>
                                {job.attempts}/{job.maxAttempts}
                            </td>
                            <td className='max-w-156 break-all'>{job.resultJson?.error || '—'}</td>
                            <td>{job.createdAt}</td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
        </div>
    );
}
