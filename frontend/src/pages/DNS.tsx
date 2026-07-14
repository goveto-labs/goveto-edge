import type {
    DNSLine,
    DNSManagedRecord,
    DNSProviderType,
    DNSSyncJob,
    UpdateDNSConfig,
} from '@/api';

import { Button, Card, Input, Label, ListBox, Select } from '@heroui/react';
import { Plus, Power, RefreshCw, Save, Trash2 } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, clusterApi, dnsApi } from '@/api';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

export default function DNS() {
    const { clusterId, clusters } = useCluster();
    const api = useMemo(() => dnsApi(clusterId), [clusterId]);
    const clusterOptionsApi = useMemo(() => clusterApi(clusterId), [clusterId]);
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
    const [lines, setLines] = useState<DNSLine[]>([]);
    const [lineName, setLineName] = useState('');
    const [lineCode, setLineCode] = useState('');
    const [loadedClusterId, setLoadedClusterId] = useState('');
    const [loading, setLoading] = useState(false);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');

    const isOwner = useMemo(
        () => clusters.find((cluster) => cluster.id === clusterId)?.role === 'OWNER',
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
        setLines([]);
        setLineName('');
        setLineCode('');
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
                const [config, recordData, jobData, lineData] = await Promise.all([
                    api.config(),
                    api.records(),
                    api.jobs(),
                    clusterOptionsApi.dnsLines(),
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
                setLines(lineData);
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
        [api, clusterId, clusterOptionsApi]
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

    const refreshLinesAndStatus = useCallback(async () => {
        const requestedClusterId = clusterId;
        if (!requestedClusterId || activeClusterRef.current !== requestedClusterId) return;
        const [lineData] = await Promise.all([clusterOptionsApi.dnsLines(), refreshStatus()]);
        if (activeClusterRef.current === requestedClusterId) setLines(lineData);
    }, [clusterId, clusterOptionsApi, refreshStatus]);

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
        if (nextProvider === 'CLOUDFLARE' && configuredProvider !== 'CLOUDFLARE') {
            setZoneId('');
            setProxied(false);
        }
    };

    const sync = async () => {
        await mutate(() => api.sync(), 'Failed to enqueue DNS sync', undefined, refreshStatus);
    };

    const disable = async () => {
        if (!confirm('Disable managed DNS and remove all records created by Goveto Edge?')) return;
        await mutate(() => api.disable(), 'Failed to disable managed DNS');
    };

    const addLine = async () => {
        if (!lineName.trim() || !lineCode.trim()) return;
        await mutate(
            () => api.createLine({ name: lineName.trim(), provider_code: lineCode.trim() }),
            'Failed to create DNS line',
            () => {
                setLineName('');
                setLineCode('');
            },
            refreshLinesAndStatus
        );
    };

    const deleteLine = async (line: DNSLine) => {
        if (!confirm(`Delete DNS line "${line.name}"?`)) return;
        await mutate(
            () => api.deleteLine(line.id),
            'Failed to delete DNS line',
            undefined,
            refreshLinesAndStatus
        );
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='Cluster hostname and site CNAME management.' title='DNS' />
                <Card className='p-8 text-center text-sm text-muted'>
                    Select a cluster to manage DNS.
                </Card>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                subtitle='Publish the cluster hostname to edge node IPs and CNAME every site domain to it.'
                title='DNS'
            >
                <Button isDisabled={loading || busy} variant='ghost' onPress={() => void load()}>
                    <RefreshCw className='mr-2 h-4 w-4' />
                    Refresh
                </Button>
                <Button
                    isDisabled={!canEdit || !configured || !enabled}
                    variant='secondary'
                    onPress={sync}
                >
                    <RefreshCw className='mr-2 h-4 w-4' />
                    Sync now
                </Button>
                {configured && enabled && isOwner && (
                    <Button isDisabled={!canEdit} variant='danger' onPress={disable}>
                        <Power className='mr-2 h-4 w-4' />
                        Disable
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
            {configured && !enabled && (
                <div className='rounded-lg border border-warning bg-warning/10 px-4 py-3 text-sm'>
                    Managed DNS is disabled. Record cleanup is processed in the background; save
                    this form to enable it again.
                </div>
            )}

            <Card className='p-5'>
                <form className='grid gap-4 md:grid-cols-2' onSubmit={save}>
                    <div className='flex flex-col gap-1'>
                        <Label htmlFor='dns-hostname'>Cluster hostname</Label>
                        <Input
                            variant='secondary'
                            id='dns-hostname'
                            disabled={!canEdit}
                            placeholder='edge.example.com'
                            required
                            value={hostname}
                            onChange={(event) => setHostname(event.target.value)}
                        />
                    </div>
                    <Select
                        variant='secondary'
                        isDisabled={!canEdit}
                        value={provider}
                        onChange={changeProvider}
                    >
                        <Label>Provider</Label>
                        <Select.Trigger>
                            <Select.Value />
                        </Select.Trigger>
                        <Select.Popover>
                            <ListBox>
                                <ListBox.Item id='ALIYUN' textValue='Aliyun DNS'>
                                    Aliyun DNS
                                </ListBox.Item>
                                <ListBox.Item id='CLOUDFLARE' textValue='Cloudflare'>
                                    Cloudflare
                                </ListBox.Item>
                            </ListBox>
                        </Select.Popover>
                    </Select>
                    <div className='flex flex-col gap-1'>
                        <Label htmlFor='dns-zone'>DNS zone</Label>
                        <Input
                            variant='secondary'
                            id='dns-zone'
                            disabled={!canEdit}
                            placeholder='example.com'
                            required
                            value={zone}
                            onChange={(event) => setZone(event.target.value)}
                        />
                    </div>
                    {provider === 'CLOUDFLARE' && (
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='dns-zone-id'>Cloudflare zone ID</Label>
                            <Input
                                variant='secondary'
                                id='dns-zone-id'
                                disabled={!canEdit}
                                required
                                value={zoneId}
                                onChange={(event) => setZoneId(event.target.value)}
                            />
                        </div>
                    )}
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
                                            ? 'Leave blank to keep current credential'
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
                                            ? 'Leave blank to keep current credential'
                                            : ''
                                    }
                                    type='password'
                                    value={accessKeySecret}
                                    onChange={(event) => setAccessKeySecret(event.target.value)}
                                />
                            </div>
                        </>
                    ) : (
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='dns-token'>API token</Label>
                            <Input
                                variant='secondary'
                                id='dns-token'
                                disabled={!canEdit}
                                placeholder={
                                    credentialsReusable ? 'Leave blank to keep current token' : ''
                                }
                                type='password'
                                value={apiToken}
                                onChange={(event) => setApiToken(event.target.value)}
                            />
                        </div>
                    )}
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
                    <div className='md:col-span-2'>
                        <Button isDisabled={!canEdit} type='submit'>
                            <Save className='mr-2 h-4 w-4' />
                            {busy ? 'Saving…' : enabled ? 'Save and sync' : 'Enable and sync'}
                        </Button>
                    </div>
                </form>
            </Card>

            <Card className='p-5'>
                <h2 className='mb-1 font-semibold'>Regional DNS lines</h2>
                <p className='mb-4 text-sm text-muted'>
                    Aliyun line codes are passed through as-is. The code “default” is reserved for
                    nodes without a line assignment. Cloudflare ignores lines.
                </p>
                {isOwner && (
                    <div className='mb-4 flex flex-col gap-2 sm:flex-row'>
                        <Input
                            variant='secondary'
                            aria-label='DNS line name'
                            disabled={!canEdit}
                            placeholder='China Telecom'
                            value={lineName}
                            onChange={(event) => setLineName(event.target.value)}
                        />
                        <Input
                            variant='secondary'
                            aria-label='DNS line code'
                            disabled={!canEdit}
                            placeholder='telecom'
                            value={lineCode}
                            onChange={(event) => setLineCode(event.target.value)}
                        />
                        <Button
                            isDisabled={!canEdit || !lineName.trim() || !lineCode.trim()}
                            onPress={addLine}
                        >
                            <Plus className='mr-2 h-4 w-4' />
                            Add line
                        </Button>
                    </div>
                )}
                <div className='space-y-2'>
                    {lines.map((line) => (
                        <div
                            className='flex items-center justify-between rounded-lg border border-border px-3 py-2'
                            key={line.id}
                        >
                            <span>
                                {line.name}
                                <code className='ml-2 text-xs text-muted'>{line.providerCode}</code>
                            </span>
                            {isOwner && (
                                <Button
                                    aria-label={`Delete DNS line ${line.name}`}
                                    isDisabled={!canEdit}
                                    isIconOnly
                                    size='sm'
                                    variant='ghost'
                                    onPress={() => void deleteLine(line)}
                                >
                                    <Trash2 className='h-4 w-4' />
                                </Button>
                            )}
                        </div>
                    ))}
                </div>
            </Card>

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
                emptyDescription='DNS synchronization jobs will appear when configuration changes.'
                emptyTitle='No DNS jobs yet'
                title='Synchronization jobs'
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
                            <td className='font-mono text-xs'>{job.id}</td>
                            <td>{job.action}</td>
                            <td>{job.status}</td>
                            <td>
                                {job.attempts}/{job.maxAttempts}
                            </td>
                            <td>{job.resultJson?.error || '—'}</td>
                            <td>{job.createdAt}</td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
        </div>
    );
}
