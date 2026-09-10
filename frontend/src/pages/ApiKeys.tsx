import type { ApiKeyPermission, ClusterApiKey, ClusterApiKeyCreated } from '@/api';

import { Alert, Button, Input } from '@heroui/react';
import {
    Check,
    Copy,
    KeyRound,
    Loader2,
    Pencil,
    Plus,
    Power,
    PowerOff,
    RefreshCcw,
    Trash2,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, apiKeysApi } from '@/api';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { useCluster } from '@/hooks/useCluster.ts';
import { canManageCluster } from '@/utils/rbac.ts';

const PERMISSION_GROUPS: { label: string; permissions: ApiKeyPermission[] }[] = [
    { label: 'Read', permissions: ['cluster.read'] },
    { label: 'Sites', permissions: ['site.write', 'site.delete'] },
    { label: 'Publish', permissions: ['site.publish'] },
    { label: 'Cache', permissions: ['cache.operate'] },
];

const PERMISSION_LABELS: Record<ApiKeyPermission, string> = {
    'cluster.read': 'Read cluster resources',
    'site.write': 'Create and update sites',
    'site.delete': 'Delete sites',
    'site.publish': 'Publish sites',
    'cache.operate': 'Purge and prewarm cache',
};

const EXPIRY_OPTIONS = [
    { id: '', label: 'Never expires' },
    { id: '30', label: '30 days' },
    { id: '90', label: '90 days' },
    { id: '365', label: '1 year' },
];

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

function formatTimestamp(value?: string) {
    if (!value) return '—';
    return new Date(value).toLocaleString();
}

function permissionColor(permission: ApiKeyPermission) {
    if (permission === 'site.delete') return 'text-danger';
    if (permission === 'site.write' || permission === 'site.publish') return 'text-warning';
    return 'text-muted';
}

function effectiveStatus(key: ClusterApiKey) {
    if (key.revoked_at) return 'REVOKED';
    if (key.expires_at && new Date(key.expires_at).getTime() <= Date.now()) return 'EXPIRED';
    return key.status;
}

function statusColor(status: ReturnType<typeof effectiveStatus>) {
    if (status === 'ACTIVE') return 'text-success';
    if (status === 'REVOKED') return 'text-danger';
    return 'text-warning';
}

function CopyField({ value }: { value: string }) {
    const [copied, setCopied] = useState(false);
    const [copyError, setCopyError] = useState('');
    return (
        <div className='space-y-1.5'>
            <div className='flex items-center gap-2'>
                <Input aria-label='Token' readOnly className='font-mono text-xs' value={value} />
                <Button
                    aria-label='Copy token'
                    variant='ghost'
                    onPress={() => {
                        setCopyError('');
                        void navigator.clipboard
                            .writeText(value)
                            .then(() => {
                                setCopied(true);
                                window.setTimeout(() => setCopied(false), 2000);
                            })
                            .catch(() => {
                                setCopied(false);
                                setCopyError('Copy failed. Select the token and copy it manually.');
                            });
                    }}
                >
                    {copied ? (
                        <Check aria-hidden='true' className='h-4 w-4 text-success' />
                    ) : (
                        <Copy aria-hidden='true' className='h-4 w-4' />
                    )}
                    {copied ? 'Copied' : 'Copy'}
                </Button>
            </div>
            {copyError && (
                <div className='text-xs text-danger' role='alert'>
                    {copyError}
                </div>
            )}
        </div>
    );
}

export default function ApiKeys() {
    const { clusterId, clusters, ready } = useCluster();
    const api = useMemo(() => apiKeysApi(clusterId), [clusterId]);
    const canManage = canManageCluster(clusters.find((item) => item.id === clusterId)?.role);

    const [keys, setKeys] = useState<ClusterApiKey[]>([]);
    const [loading, setLoading] = useState(true);
    const [busy, setBusy] = useState('');
    const [error, setError] = useState('');
    const [success, setSuccess] = useState('');

    const [createOpen, setCreateOpen] = useState(false);
    const [name, setName] = useState('');
    const [selectedPermissions, setSelectedPermissions] = useState<ApiKeyPermission[]>([
        'cluster.read',
    ]);
    const [expiryDays, setExpiryDays] = useState('');
    const [issued, setIssued] = useState<ClusterApiKeyCreated | null>(null);

    const [editing, setEditing] = useState<ClusterApiKey | null>(null);
    const [editName, setEditName] = useState('');
    const [editPermissions, setEditPermissions] = useState<ApiKeyPermission[]>([]);
    const [revokeTarget, setRevokeTarget] = useState<ClusterApiKey | null>(null);
    const [rotateTarget, setRotateTarget] = useState<ClusterApiKey | null>(null);
    const [disableTarget, setDisableTarget] = useState<ClusterApiKey | null>(null);
    const clusterGeneration = useRef(0);

    const load = useCallback(async () => {
        const generation = clusterGeneration.current;
        if (!clusterId) {
            setKeys([]);
            return;
        }
        setLoading(true);
        setError('');
        try {
            const result = await api.list({ page_size: 200 });
            if (generation === clusterGeneration.current) setKeys(result.items);
        } catch (loadError) {
            if (generation === clusterGeneration.current) {
                setError(errorMessage(loadError, 'Failed to load API keys'));
            }
        } finally {
            if (generation === clusterGeneration.current) setLoading(false);
        }
    }, [api, clusterId]);

    useEffect(() => {
        clusterGeneration.current += 1;
        setKeys([]);
        setLoading(Boolean(clusterId));
        setBusy('');
        setError('');
        setSuccess('');
        setCreateOpen(false);
        setIssued(null);
        setEditing(null);
        setRotateTarget(null);
        setRevokeTarget(null);
        setDisableTarget(null);
    }, [clusterId]);

    useEffect(() => {
        if (ready) void load();
    }, [load, ready]);

    const togglePermission = (
        permission: ApiKeyPermission,
        current: ApiKeyPermission[],
        setter: (next: ApiKeyPermission[]) => void
    ) => {
        setter(
            current.includes(permission)
                ? current.filter((item) => item !== permission)
                : [...current, permission]
        );
    };

    const create = async (event: React.FormEvent) => {
        event.preventDefault();
        const generation = clusterGeneration.current;
        setBusy('create');
        setError('');
        try {
            const created = await api.create({
                name: name.trim(),
                permissions: selectedPermissions,
                ...(expiryDays ? { expires_in_days: Number(expiryDays) } : {}),
            });
            if (generation !== clusterGeneration.current) return;
            setCreateOpen(false);
            setName('');
            setSelectedPermissions(['cluster.read']);
            setExpiryDays('');
            setIssued(created);
            await load();
        } catch (createError) {
            if (generation === clusterGeneration.current) {
                setError(errorMessage(createError, 'Failed to create API key'));
            }
        } finally {
            if (generation === clusterGeneration.current) setBusy('');
        }
    };

    const update = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!editing) return;
        const generation = clusterGeneration.current;
        setBusy('update');
        setError('');
        try {
            await api.update(editing.id, {
                name: editName.trim(),
                permissions: editPermissions,
            });
            if (generation !== clusterGeneration.current) return;
            setEditing(null);
            setSuccess('API key updated.');
            await load();
        } catch (updateError) {
            if (generation === clusterGeneration.current) {
                setError(errorMessage(updateError, 'Failed to update API key'));
            }
        } finally {
            if (generation === clusterGeneration.current) setBusy('');
        }
    };

    const setStatus = async (key: ClusterApiKey, status: 'ACTIVE' | 'DISABLED') => {
        const generation = clusterGeneration.current;
        setBusy(key.id);
        setError('');
        try {
            await api.update(key.id, { status });
            if (generation !== clusterGeneration.current) return;
            if (status === 'DISABLED') setDisableTarget(null);
            setSuccess(`API key "${key.name}" ${status === 'ACTIVE' ? 'enabled' : 'disabled'}.`);
            await load();
        } catch (statusError) {
            if (generation === clusterGeneration.current) {
                setError(errorMessage(statusError, 'Failed to update API key'));
            }
        } finally {
            if (generation === clusterGeneration.current) setBusy('');
        }
    };

    const rotate = async () => {
        if (!rotateTarget) return;
        const generation = clusterGeneration.current;
        setBusy('rotate');
        setError('');
        try {
            const rotated = await api.rotate(rotateTarget.id);
            if (generation !== clusterGeneration.current) return;
            setRotateTarget(null);
            setIssued(rotated);
            await load();
        } catch (rotateError) {
            if (generation === clusterGeneration.current) {
                setError(errorMessage(rotateError, 'Failed to rotate API key'));
            }
        } finally {
            if (generation === clusterGeneration.current) setBusy('');
        }
    };

    const revoke = async () => {
        if (!revokeTarget) return;
        const generation = clusterGeneration.current;
        setBusy('revoke');
        setError('');
        try {
            await api.revoke(revokeTarget.id);
            if (generation !== clusterGeneration.current) return;
            setRevokeTarget(null);
            setSuccess(`API key "${revokeTarget.name}" revoked.`);
            await load();
        } catch (revokeError) {
            if (generation === clusterGeneration.current) {
                setError(errorMessage(revokeError, 'Failed to revoke API key'));
            }
        } finally {
            if (generation === clusterGeneration.current) setBusy('');
        }
    };

    return (
        <div className='space-y-5'>
            <PageHeader
                actions={
                    <Button
                        isDisabled={!clusterId || !canManage}
                        onPress={() => {
                            setError('');
                            setCreateOpen(true);
                        }}
                    >
                        <Plus aria-hidden='true' className='h-4 w-4' /> Create API key
                    </Button>
                }
                subtitle='Automate cluster operations with scoped API keys.'
                title='API keys'
            />
            {error &&
                !createOpen &&
                !editing &&
                !issued &&
                !rotateTarget &&
                !revokeTarget &&
                !disableTarget && <FormError message={error} />}
            {success && !issued && (
                <Alert status='success'>
                    <Alert.Indicator />
                    <Alert.Content>
                        <Alert.Title>API keys updated</Alert.Title>
                        <Alert.Description>{success}</Alert.Description>
                    </Alert.Content>
                </Alert>
            )}
            {!clusterId ? (
                <div className='py-12 text-center text-sm text-muted'>
                    Select a cluster to manage API keys.
                </div>
            ) : (
                <DataTable
                    aria-label='Cluster API keys'
                    empty={keys.length === 0}
                    emptyDescription='Create an API key to automate site and cache operations.'
                    emptyTitle='No API keys'
                    loading={loading && keys.length === 0}
                    title={`${keys.length} API keys`}
                >
                    <thead>
                        <tr>
                            <th>Name</th>
                            <th>Status</th>
                            <th>Permissions</th>
                            <th>Last used</th>
                            <th>Expires</th>
                            <th aria-label='Actions' />
                        </tr>
                    </thead>
                    <tbody>
                        {keys.map((key) => {
                            const displayStatus = effectiveStatus(key);
                            return (
                                <tr key={key.id}>
                                    <td>
                                        <div className='text-sm font-medium'>{key.name}</div>
                                        <div className='font-mono text-xs text-muted'>
                                            {key.prefix}…
                                        </div>
                                    </td>
                                    <td>
                                        <span className={statusColor(displayStatus)}>
                                            {displayStatus}
                                        </span>
                                    </td>
                                    <td>
                                        <div className='flex max-w-xs flex-wrap gap-1'>
                                            {key.permissions.map((permission) => (
                                                <span
                                                    key={permission}
                                                    className={`rounded border border-border bg-surface-secondary px-1.5 py-0.5 font-mono text-[11px] ${permissionColor(permission)}`}
                                                >
                                                    {permission}
                                                </span>
                                            ))}
                                        </div>
                                    </td>
                                    <td className='whitespace-nowrap text-xs text-muted'>
                                        {formatTimestamp(key.last_used_at)}
                                    </td>
                                    <td className='whitespace-nowrap text-xs text-muted'>
                                        {formatTimestamp(key.expires_at)}
                                    </td>
                                    <td>
                                        {canManage && !key.revoked_at && (
                                            <div className='flex items-center justify-end gap-1'>
                                                {displayStatus !== 'EXPIRED' && (
                                                    <Button
                                                        isIconOnly
                                                        aria-label={`${key.status === 'ACTIVE' ? 'Disable' : 'Enable'} ${key.name}`}
                                                        isDisabled={Boolean(busy)}
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() => {
                                                            setError('');
                                                            if (key.status === 'ACTIVE') {
                                                                setDisableTarget(key);
                                                            } else {
                                                                void setStatus(key, 'ACTIVE');
                                                            }
                                                        }}
                                                    >
                                                        {key.status === 'ACTIVE' ? (
                                                            <PowerOff
                                                                aria-hidden='true'
                                                                className='h-4 w-4'
                                                            />
                                                        ) : (
                                                            <Power
                                                                aria-hidden='true'
                                                                className='h-4 w-4'
                                                            />
                                                        )}
                                                    </Button>
                                                )}
                                                <Button
                                                    isIconOnly
                                                    aria-label={`Edit ${key.name}`}
                                                    isDisabled={Boolean(busy)}
                                                    size='sm'
                                                    variant='ghost'
                                                    onPress={() => {
                                                        setEditName(key.name);
                                                        setEditPermissions(key.permissions);
                                                        setEditing(key);
                                                        setError('');
                                                    }}
                                                >
                                                    <Pencil
                                                        aria-hidden='true'
                                                        className='h-4 w-4'
                                                    />
                                                </Button>
                                                {displayStatus === 'ACTIVE' && (
                                                    <Button
                                                        isIconOnly
                                                        aria-label={`Rotate ${key.name}`}
                                                        isDisabled={Boolean(busy)}
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() => {
                                                            setError('');
                                                            setRotateTarget(key);
                                                        }}
                                                    >
                                                        <RefreshCcw
                                                            aria-hidden='true'
                                                            className='h-4 w-4'
                                                        />
                                                    </Button>
                                                )}
                                                <Button
                                                    isIconOnly
                                                    aria-label={`Revoke ${key.name}`}
                                                    isDisabled={Boolean(busy)}
                                                    size='sm'
                                                    variant='ghost'
                                                    onPress={() => {
                                                        setError('');
                                                        setRevokeTarget(key);
                                                    }}
                                                >
                                                    <Trash2
                                                        aria-hidden='true'
                                                        className='h-4 w-4 text-danger'
                                                    />
                                                </Button>
                                            </div>
                                        )}
                                    </td>
                                </tr>
                            );
                        })}
                    </tbody>
                </DataTable>
            )}

            <DialogShell
                clusterContext='target'
                icon={<KeyRound className='h-5 w-5' />}
                isOpen={createOpen}
                size='md'
                subtitle='Keys are bound to this cluster and hold only the permissions you grant.'
                title='Create API key'
                onOpenChange={setCreateOpen}
            >
                <form onSubmit={create}>
                    <div className='space-y-4 px-6 py-5'>
                        <FormField hint='Shown in lists and audit logs.' label='Name'>
                            <Input
                                autoComplete='off'
                                maxLength={80}
                                placeholder='terraform'
                                required
                                variant='secondary'
                                value={name}
                                onChange={(event) => setName(event.target.value)}
                            />
                        </FormField>
                        <FormField
                            hint='Automation can never manage keys, nodes or credentials.'
                            label='Permissions'
                        >
                            <div className='space-y-3'>
                                {PERMISSION_GROUPS.map((group) => (
                                    <div key={group.label}>
                                        <div className='mb-1.5 text-xs font-semibold uppercase tracking-wide text-muted'>
                                            {group.label}
                                        </div>
                                        <div className='space-y-1.5'>
                                            {group.permissions.map((permission) => (
                                                <label
                                                    key={permission}
                                                    className='flex cursor-pointer items-center gap-2 text-sm'
                                                >
                                                    <input
                                                        checked={selectedPermissions.includes(
                                                            permission
                                                        )}
                                                        type='checkbox'
                                                        onChange={() =>
                                                            togglePermission(
                                                                permission,
                                                                selectedPermissions,
                                                                setSelectedPermissions
                                                            )
                                                        }
                                                    />
                                                    <span className='font-mono text-xs text-muted'>
                                                        {permission}
                                                    </span>
                                                    <span className='text-muted'>
                                                        {PERMISSION_LABELS[permission]}
                                                    </span>
                                                </label>
                                            ))}
                                        </div>
                                    </div>
                                ))}
                            </div>
                        </FormField>
                        <FormField label='Expiry'>
                            <SelectField
                                variant='secondary'
                                options={EXPIRY_OPTIONS}
                                value={expiryDays}
                                onChange={setExpiryDays}
                            />
                        </FormField>
                        {error && <FormError message={error} />}
                    </div>
                    <DialogFooter>
                        <Button onPress={() => setCreateOpen(false)} type='button' variant='ghost'>
                            Cancel
                        </Button>
                        <Button
                            isDisabled={
                                busy === 'create' ||
                                !name.trim() ||
                                selectedPermissions.length === 0
                            }
                            type='submit'
                        >
                            {busy === 'create' && (
                                <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                            )}
                            Create key
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <DialogShell
                clusterContext='target'
                icon={<KeyRound className='h-5 w-5' />}
                isOpen={Boolean(editing)}
                size='md'
                subtitle='Changes apply to new requests immediately.'
                title='Edit API key'
                onOpenChange={(open) => {
                    if (!open) setEditing(null);
                }}
            >
                <form onSubmit={update}>
                    <div className='space-y-4 px-6 py-5'>
                        <FormField label='Name'>
                            <Input
                                autoComplete='off'
                                maxLength={80}
                                required
                                value={editName}
                                onChange={(event) => setEditName(event.target.value)}
                            />
                        </FormField>
                        <FormField label='Permissions'>
                            <div className='space-y-1.5'>
                                {PERMISSION_GROUPS.flatMap((group) => group.permissions).map(
                                    (permission) => (
                                        <label
                                            key={permission}
                                            className='flex cursor-pointer items-center gap-2 text-sm'
                                        >
                                            <input
                                                checked={editPermissions.includes(permission)}
                                                type='checkbox'
                                                onChange={() =>
                                                    togglePermission(
                                                        permission,
                                                        editPermissions,
                                                        setEditPermissions
                                                    )
                                                }
                                            />
                                            <span className='font-mono text-xs text-muted'>
                                                {permission}
                                            </span>
                                            <span className='text-muted'>
                                                {PERMISSION_LABELS[permission]}
                                            </span>
                                        </label>
                                    )
                                )}
                            </div>
                        </FormField>
                        {error && <FormError message={error} />}
                    </div>
                    <DialogFooter>
                        <Button onPress={() => setEditing(null)} type='button' variant='ghost'>
                            Cancel
                        </Button>
                        <Button
                            isDisabled={
                                busy === 'update' ||
                                !editName.trim() ||
                                editPermissions.length === 0
                            }
                            type='submit'
                        >
                            {busy === 'update' && (
                                <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                            )}
                            Save changes
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <DialogShell
                icon={<KeyRound className='h-5 w-5' />}
                isOpen={Boolean(issued)}
                isDismissable={false}
                size='md'
                subtitle='This is the only time the token is shown. Store it now.'
                title='API key ready'
                onOpenChange={(open) => {
                    if (!open) setIssued(null);
                }}
            >
                <div className='space-y-4 px-6 py-5'>
                    <div className='rounded-lg border border-warning/30 bg-warning/10 px-4 py-3 text-sm text-warning'>
                        Copy the token before closing this dialog. It cannot be recovered later.
                    </div>
                    <FormField
                        hint='Send as "Authorization: Bearer …" or "X-API-Key".'
                        label='Token'
                    >
                        <CopyField value={issued?.token ?? ''} />
                    </FormField>
                    <div className='text-xs text-muted'>
                        Name <span className='font-mono'>{issued?.name}</span> · prefix{' '}
                        <span className='font-mono'>{issued?.prefix}…</span> · permissions{' '}
                        <span className='font-mono'>{issued?.permissions.join(', ')}</span>
                    </div>
                    {error && <FormError message={error} />}
                </div>
                <DialogFooter>
                    <Button
                        onPress={() => {
                            setIssued(null);
                        }}
                    >
                        I stored the token
                    </Button>
                </DialogFooter>
            </DialogShell>

            <ConfirmDialog
                clusterName={clusters.find((item) => item.id === clusterId)?.name}
                confirmLabel='Disable key'
                description={`Disable "${disableTarget?.name}". Requests using this token will stop immediately.`}
                error={error}
                impact='Automation using this key loses access until the key is enabled again.'
                isOpen={Boolean(disableTarget)}
                loading={Boolean(disableTarget && busy === disableTarget.id)}
                recoverability='The same token works again after the key is enabled.'
                title='Disable API key'
                onConfirm={() => {
                    if (disableTarget) void setStatus(disableTarget, 'DISABLED');
                }}
                onOpenChange={(open) => {
                    if (!open) setDisableTarget(null);
                }}
            />

            <ConfirmDialog
                clusterName={clusters.find((item) => item.id === clusterId)?.name}
                confirmLabel='Rotate key'
                description={`Replace the token of "${rotateTarget?.name}". The previous token keeps working for 10 minutes so automation can cut over safely.`}
                error={error}
                impact='Requests using the old token fail after the grace window.'
                isOpen={Boolean(rotateTarget)}
                loading={busy === 'rotate'}
                recoverability='The new token works immediately; the old one expires after the grace window.'
                title='Rotate API key'
                onConfirm={rotate}
                onOpenChange={(open) => {
                    if (!open) setRotateTarget(null);
                }}
            />

            <ConfirmDialog
                clusterName={clusters.find((item) => item.id === clusterId)?.name}
                confirmationText={revokeTarget?.name ?? ''}
                confirmLabel='Revoke key'
                danger
                description={`Permanently revoke "${revokeTarget?.name}". Requests using its token will fail immediately.`}
                error={error}
                impact='All automation using this key stops working.'
                isOpen={Boolean(revokeTarget)}
                loading={busy === 'revoke'}
                recoverability='Revocation is permanent; create a new key instead.'
                title='Revoke API key'
                onConfirm={revoke}
                onOpenChange={(open) => {
                    if (!open) setRevokeTarget(null);
                }}
            />
        </div>
    );
}
