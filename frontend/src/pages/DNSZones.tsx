import type { CreateDNSZoneRequest, DNSProviderDomain, DNSProviderType, DNSZone } from '@/api';

import { Button, Input, Label, Spinner } from '@heroui/react';
import { Plus, RefreshCw, Save, Shield, Trash2 } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';

import { ApiError, dnsApi } from '@/api';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { canManageCluster } from '@/utils/rbac.ts';

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

function providerLabel(provider: DNSProviderType) {
    return provider === 'ALIYUN' ? 'Aliyun DNS' : 'Cloudflare';
}

export default function DNSZones() {
    const { clusterId, clusters } = useCluster();
    const api = useMemo(() => dnsApi(clusterId), [clusterId]);
    const isOwner = canManageCluster(clusters.find((cluster) => cluster.id === clusterId)?.role);
    const [zones, setZones] = useState<DNSZone[]>([]);
    const [loading, setLoading] = useState(true);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');
    const [dialogOpen, setDialogOpen] = useState(false);
    const [pendingDelete, setPendingDelete] = useState<DNSZone | null>(null);
    const [provider, setProvider] = useState<DNSProviderType>('ALIYUN');
    const [accessKeyId, setAccessKeyId] = useState('');
    const [accessKeySecret, setAccessKeySecret] = useState('');
    const [apiToken, setApiToken] = useState('');
    const [zoneName, setZoneName] = useState('');
    const [zoneId, setZoneId] = useState('');
    const [providerDomains, setProviderDomains] = useState<DNSProviderDomain[]>([]);
    const [discovering, setDiscovering] = useState(false);
    const canEdit = Boolean(clusterId) && isOwner && !loading && !busy;

    const load = useCallback(async () => {
        if (!clusterId) {
            setZones([]);
            setLoading(false);
            return;
        }
        setLoading(true);
        try {
            setZones(await api.zones());
            setError('');
        } catch (loadError) {
            setError(errorMessage(loadError, 'Failed to load DNS zones'));
        } finally {
            setLoading(false);
        }
    }, [api, clusterId]);

    useAutoRefresh(load, Boolean(clusterId));

    const resetDialog = () => {
        setProvider('ALIYUN');
        setAccessKeyId('');
        setAccessKeySecret('');
        setApiToken('');
        setZoneName('');
        setZoneId('');
        setProviderDomains([]);
        setDiscovering(false);
    };

    const openDialog = () => {
        resetDialog();
        setError('');
        setDialogOpen(true);
    };

    const credentials = (): Record<string, string> | null => {
        if (provider === 'ALIYUN') {
            if (!accessKeyId || !accessKeySecret) {
                setError('Aliyun AccessKey ID and secret are required.');
                return null;
            }
            return { access_key_id: accessKeyId, access_key_secret: accessKeySecret };
        }
        if (!apiToken) {
            setError('Cloudflare API token is required.');
            return null;
        }
        return { api_token: apiToken };
    };

    const discoverDomains = async () => {
        const providerCredentials = credentials();
        if (!providerCredentials) return;
        setDiscovering(true);
        setError('');
        try {
            const available = await api.discoverDomains({
                provider,
                credentials: providerCredentials,
            });
            setProviderDomains(available);
            if (available.length === 0) setError('No zones are available for this credential.');
        } catch (discoverError) {
            setProviderDomains([]);
            setError(errorMessage(discoverError, 'Failed to load provider zones'));
        } finally {
            setDiscovering(false);
        }
    };

    const selectDomain = (key: React.Key | null) => {
        if (!key) return;
        const domain = providerDomains.find((item) => item.name === String(key));
        if (!domain) return;
        setZoneName(domain.name);
        setZoneId(domain.id ?? '');
    };

    const save = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!canEdit || !zoneName) return;
        const providerCredentials = credentials();
        if (!providerCredentials) return;
        const payload: CreateDNSZoneRequest = {
            provider,
            zone: zoneName,
            credentials: providerCredentials,
            enabled: true,
        };
        if (provider === 'CLOUDFLARE') payload.zone_id = zoneId;

        setBusy(true);
        setError('');
        try {
            await api.createZone(payload);
            setDialogOpen(false);
            resetDialog();
            await load();
        } catch (saveError) {
            setError(errorMessage(saveError, 'Failed to add DNS zone'));
        } finally {
            setBusy(false);
        }
    };

    const remove = async (zone: DNSZone) => {
        setBusy(true);
        setError('');
        try {
            await api.deleteZone(zone.id);
            await load();
        } catch (deleteError) {
            setError(errorMessage(deleteError, 'Failed to delete DNS zone'));
        } finally {
            setBusy(false);
        }
    };

    return (
        <div className='space-y-6'>
            <PageHeader
                subtitle='Manage provider zones used for ACME DNS-01 certificate issuance and renewal.'
                title='DNS zones'
            >
                <Button
                    isDisabled={!clusterId || loading || busy}
                    variant='ghost'
                    onPress={() => void load()}
                >
                    <RefreshCw className='mr-2 h-4 w-4' />
                    Refresh
                </Button>
                {isOwner && (
                    <Button isDisabled={!canEdit} onPress={openDialog}>
                        <Plus className='mr-2 h-4 w-4' />
                        Add zone
                    </Button>
                )}
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}
            {!clusterId && (
                <div className='rounded-lg border border-border bg-surface-secondary px-4 py-3 text-sm text-muted'>
                    Select a cluster to manage DNS zones.
                </div>
            )}
            {clusterId && !isOwner && (
                <div className='rounded-lg border border-border bg-surface-secondary px-4 py-3 text-sm text-muted'>
                    DNS zones are read-only because only the cluster owner can manage provider
                    credentials.
                </div>
            )}

            <DataTable
                aria-label='DNS zones'
                empty={zones.length === 0}
                emptyAction={
                    isOwner && clusterId ? (
                        <Button isDisabled={!canEdit} onPress={openDialog}>
                            <Plus className='mr-2 h-4 w-4' />
                            Add zone
                        </Button>
                    ) : undefined
                }
                emptyDescription='Add a provider zone before issuing wildcard certificates or using DNS-01 validation.'
                emptyTitle='No DNS zones'
                loading={loading && Boolean(clusterId)}
            >
                <thead>
                    <tr>
                        <th>Zone</th>
                        <th>Provider</th>
                        <th>Purpose</th>
                        <th>Status</th>
                        <th>Updated</th>
                        <th className='text-right'>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    {zones.map((zone) => (
                        <tr key={zone.id}>
                            <td>
                                <div className='flex items-center gap-2 font-mono text-xs font-semibold'>
                                    <Shield className='h-4 w-4 text-muted' />
                                    {zone.zone}
                                </div>
                            </td>
                            <td className='text-sm'>{providerLabel(zone.type)}</td>
                            <td className='text-sm'>
                                {zone.kind === 'ENDPOINT'
                                    ? 'Cluster endpoint and ACME DNS-01'
                                    : 'ACME DNS-01'}
                            </td>
                            <td>
                                <StatusBadge status={zone.enabled ? 'ACTIVE' : 'DISABLED'} />
                            </td>
                            <td className='whitespace-nowrap text-sm text-muted'>
                                {new Date(zone.updated_at).toLocaleString()}
                            </td>
                            <td>
                                <div className='flex justify-end'>
                                    {zone.kind === 'ACME' && isOwner ? (
                                        <Button
                                            isIconOnly
                                            aria-label={`Delete ${zone.zone}`}
                                            isDisabled={!canEdit}
                                            size='sm'
                                            variant='danger'
                                            onPress={() => setPendingDelete(zone)}
                                        >
                                            <Trash2 className='h-3.5 w-3.5' />
                                        </Button>
                                    ) : (
                                        <span className='text-xs text-muted'>Managed in DNS</span>
                                    )}
                                </div>
                            </td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>

            <DialogShell
                icon={<Shield className='h-5 w-5' />}
                isDismissable={!busy}
                isOpen={dialogOpen}
                size='lg'
                subtitle='Connect a provider zone for ACME DNS-01 challenges.'
                title='Add DNS zone'
                onOpenChange={(open) => {
                    setDialogOpen(open);
                    if (!open) resetDialog();
                }}
            >
                <form onSubmit={save}>
                    <div className='grid max-h-[70vh] gap-4 overflow-y-auto px-6 py-5 md:grid-cols-2'>
                        <SelectField
                            isDisabled={!canEdit}
                            label='Provider'
                            options={[
                                { id: 'ALIYUN', label: 'Aliyun DNS' },
                                { id: 'CLOUDFLARE', label: 'Cloudflare' },
                            ]}
                            value={provider}
                            variant='secondary'
                            onChange={(key) => {
                                if (!key) return;
                                setProvider(String(key) as DNSProviderType);
                                setAccessKeyId('');
                                setAccessKeySecret('');
                                setApiToken('');
                                setZoneName('');
                                setZoneId('');
                                setProviderDomains([]);
                            }}
                        />
                        <div className='hidden md:block' />
                        {provider === 'ALIYUN' ? (
                            <>
                                <div className='flex flex-col gap-1'>
                                    <Label htmlFor='zone-key-id'>AccessKey ID</Label>
                                    <Input
                                        disabled={!canEdit}
                                        id='zone-key-id'
                                        value={accessKeyId}
                                        variant='secondary'
                                        onChange={(event) => setAccessKeyId(event.target.value)}
                                    />
                                </div>
                                <div className='flex flex-col gap-1'>
                                    <Label htmlFor='zone-key-secret'>AccessKey secret</Label>
                                    <Input
                                        disabled={!canEdit}
                                        id='zone-key-secret'
                                        type='password'
                                        value={accessKeySecret}
                                        variant='secondary'
                                        onChange={(event) => setAccessKeySecret(event.target.value)}
                                    />
                                </div>
                            </>
                        ) : (
                            <div className='flex flex-col gap-1 md:col-span-2'>
                                <Label htmlFor='zone-api-token'>API token</Label>
                                <Input
                                    disabled={!canEdit}
                                    id='zone-api-token'
                                    type='password'
                                    value={apiToken}
                                    variant='secondary'
                                    onChange={(event) => setApiToken(event.target.value)}
                                />
                            </div>
                        )}
                        <div className='md:col-span-2 flex flex-row items-end gap-3'>
                            <Button
                                isDisabled={!canEdit || discovering}
                                variant='secondary'
                                onPress={() => void discoverDomains()}
                            >
                                {discovering ? (
                                    <Spinner className='mr-2' size='sm' />
                                ) : (
                                    <RefreshCw className='mr-2 h-4 w-4' />
                                )}
                                Load zones
                            </Button>
                            <SelectField
                                isDisabled={!canEdit || providerDomains.length === 0}
                                label='DNS zone'
                                options={providerDomains.map((domain) => ({
                                    id: domain.name,
                                    label: domain.name,
                                }))}
                                className='flex-1'
                                placeholder='Select a DNS zone'
                                value={zoneName}
                                variant='secondary'
                                onChange={selectDomain}
                            />
                        </div>

                        <div className='rounded-lg border border-border bg-surface-secondary px-4 py-3 text-sm text-muted md:col-span-2'>
                            This zone creates temporary ACME TXT records and does not change the
                            cluster scheduling hostname.
                        </div>
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
                            onPress={() => setDialogOpen(false)}
                        >
                            Cancel
                        </Button>
                        <Button isDisabled={!canEdit || !zoneName} type='submit'>
                            <Save className='mr-2 h-4 w-4' />
                            {busy ? 'Saving…' : 'Add zone'}
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <ConfirmDialog
                confirmLabel='Delete'
                danger
                description={
                    pendingDelete
                        ? `Remove DNS zone "${pendingDelete.zone}"? Existing certificates keep working until the next DNS-01 renewal.`
                        : undefined
                }
                isOpen={pendingDelete !== null}
                title='Delete DNS zone?'
                onConfirm={() => {
                    const zone = pendingDelete;
                    setPendingDelete(null);
                    if (zone) void remove(zone);
                }}
                onOpenChange={(open) => {
                    if (!open) setPendingDelete(null);
                }}
            />
        </div>
    );
}
