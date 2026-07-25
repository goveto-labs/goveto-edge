import type { SSHCredential, SSHCredentialNode, SSHCredentialWriteRequest } from '@/api';

import { Button } from '@heroui/react';
import { Eye, KeyRound, Pencil, Plus, Server, Trash2 } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, sshCredentialsApi } from '@/api';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogShell } from '@/components/DialogShell.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SSHCredentialDialog } from '@/components/SSHCredentialDialog.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { canManageCluster } from '@/utils/rbac.ts';

export default function SSHCredentials() {
    const navigate = useNavigate();
    const { clusterId, clusters } = useCluster();
    const api = useMemo(() => sshCredentialsApi(clusterId), [clusterId]);
    const isOwner = canManageCluster(clusters.find((cluster) => cluster.id === clusterId)?.role);
    const [credentials, setCredentials] = useState<SSHCredential[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [dialogOpen, setDialogOpen] = useState(false);
    const [editing, setEditing] = useState<SSHCredential | null>(null);
    const [nodesOpen, setNodesOpen] = useState(false);
    const [nodesLoading, setNodesLoading] = useState(false);
    const [linkedNodes, setLinkedNodes] = useState<SSHCredentialNode[]>([]);
    const [viewing, setViewing] = useState<SSHCredential | null>(null);

    const load = useCallback(async () => {
        if (!clusterId || !isOwner) {
            setCredentials([]);
            setLoading(false);
            return;
        }
        try {
            setCredentials(await api.list());
            setError('');
        } catch (loadError) {
            setError(
                loadError instanceof ApiError ? loadError.message : 'Failed to load SSH credentials'
            );
        } finally {
            setLoading(false);
        }
    }, [api, clusterId, isOwner]);

    useAutoRefresh(load, Boolean(clusterId && isOwner));

    const openNodes = async (credential: SSHCredential) => {
        setViewing(credential);
        setLinkedNodes([]);
        setNodesOpen(true);
        setNodesLoading(true);
        try {
            setLinkedNodes(await api.nodes(credential.id));
        } catch (nodesError) {
            setError(
                nodesError instanceof ApiError ? nodesError.message : 'Failed to load linked nodes'
            );
        } finally {
            setNodesLoading(false);
        }
    };

    const remove = async (credential: SSHCredential) => {
        if (!window.confirm(`Delete SSH credential "${credential.name}"? This cannot be undone.`)) {
            return;
        }
        try {
            await api.delete(credential.id);
            await load();
        } catch (deleteError) {
            setError(
                deleteError instanceof ApiError
                    ? deleteError.message
                    : 'Failed to delete SSH credential'
            );
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader title='SSH credentials' />
                <DataTable empty emptyDescription='Select a cluster to manage SSH credentials.' />
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                subtitle='Store reusable encrypted SSH authentication for node installation.'
                title='SSH credentials'
            >
                {isOwner && (
                    <Button
                        onPress={() => {
                            setEditing(null);
                            setDialogOpen(true);
                        }}
                    >
                        <Plus className='mr-2 h-4 w-4' />
                        Add credential
                    </Button>
                )}
            </PageHeader>

            {!isOwner && (
                <div className='rounded-xl border border-border bg-surface-secondary/40 px-4 py-3 text-sm text-muted'>
                    Only the cluster owner can view or manage SSH credentials.
                </div>
            )}
            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <DataTable
                aria-label='SSH credentials'
                empty={credentials.length === 0}
                emptyAction={
                    isOwner ? (
                        <Button
                            onPress={() => {
                                setEditing(null);
                                setDialogOpen(true);
                            }}
                        >
                            <Plus className='mr-2 h-4 w-4' />
                            Add credential
                        </Button>
                    ) : undefined
                }
                emptyDescription={
                    isOwner
                        ? 'Add a credential before installing a node over SSH.'
                        : 'Ask the cluster owner to add an SSH credential.'
                }
                emptyTitle='No SSH credentials'
                loading={loading}
            >
                <thead>
                    <tr>
                        <th>Name</th>
                        <th>SSH user</th>
                        <th>Authentication</th>
                        <th>Used by nodes</th>
                        <th>Updated</th>
                        <th className='text-right'>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    {credentials.map((credential) => (
                        <tr key={credential.id}>
                            <td>
                                <div className='flex items-center gap-2 text-sm font-semibold'>
                                    <KeyRound className='h-4 w-4 text-muted' />
                                    {credential.name}
                                </div>
                            </td>
                            <td className='font-mono text-xs'>{credential.username}</td>
                            <td className='text-sm'>
                                {credential.auth_type === 'PASSWORD' ? 'Password' : 'Private key'}
                            </td>
                            <td>
                                <Button
                                    size='sm'
                                    variant='ghost'
                                    onPress={() => void openNodes(credential)}
                                >
                                    <Server className='mr-1.5 h-3.5 w-3.5' />
                                    {credential.node_count}
                                </Button>
                            </td>
                            <td className='whitespace-nowrap text-sm text-muted'>
                                {new Date(credential.updated_at).toLocaleString()}
                            </td>
                            <td>
                                <div className='flex justify-end gap-2'>
                                    <Button
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => void openNodes(credential)}
                                    >
                                        <Eye className='mr-1.5 h-3.5 w-3.5' />
                                        Nodes
                                    </Button>
                                    {isOwner && (
                                        <>
                                            <Button
                                                size='sm'
                                                variant='secondary'
                                                onPress={() => {
                                                    setEditing(credential);
                                                    setDialogOpen(true);
                                                }}
                                            >
                                                <Pencil className='mr-1.5 h-3.5 w-3.5' />
                                                Edit
                                            </Button>
                                            <Button
                                                isDisabled={credential.node_count > 0}
                                                size='sm'
                                                variant='danger'
                                                onPress={() => void remove(credential)}
                                            >
                                                <Trash2 className='mr-1.5 h-3.5 w-3.5' />
                                                Delete
                                            </Button>
                                        </>
                                    )}
                                </div>
                            </td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>

            <SSHCredentialDialog
                credential={editing}
                isOpen={dialogOpen}
                onOpenChange={setDialogOpen}
                onSave={(payload: SSHCredentialWriteRequest) =>
                    editing ? api.update(editing.id, payload) : api.create(payload)
                }
                onSaved={() => void load()}
            />

            <DialogShell
                icon={<Server className='h-4 w-4' />}
                isOpen={nodesOpen}
                title={viewing ? `Nodes using ${viewing.name}` : 'Linked nodes'}
                onOpenChange={setNodesOpen}
            >
                <div className='max-h-[60vh] overflow-y-auto px-6 py-5'>
                    {nodesLoading ? (
                        <p className='text-sm text-muted'>Loading nodes…</p>
                    ) : linkedNodes.length === 0 ? (
                        <p className='text-sm text-muted'>
                            This credential is not used by any node.
                        </p>
                    ) : (
                        <div className='space-y-2'>
                            {linkedNodes.map((node) => (
                                <button
                                    className='flex w-full items-center justify-between rounded-xl border border-border px-4 py-3 text-left hover:bg-surface-secondary'
                                    key={node.id}
                                    type='button'
                                    onClick={() => navigate(`/nodes/${node.id}/installation`)}
                                >
                                    <span className='text-sm font-semibold'>{node.name}</span>
                                    <StatusBadge status={node.status} />
                                </button>
                            ))}
                        </div>
                    )}
                </div>
            </DialogShell>
        </div>
    );
}
