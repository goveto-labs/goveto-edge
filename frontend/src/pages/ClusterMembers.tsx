import type { ClusterMember, ClusterRole } from '@/api';

import { Button, Input } from '@heroui/react';
import { Loader2, Pencil, Plus, Trash2, Users } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, clusterApi } from '@/api';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { useCluster } from '@/hooks/useCluster.ts';
import { canManageCluster } from '@/utils/rbac.ts';

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

export default function ClusterMembers() {
    const { clusterId, clusters, ready } = useCluster();
    const api = useMemo(() => clusterApi(clusterId), [clusterId]);
    const canManage = canManageCluster(clusters.find((item) => item.id === clusterId)?.role);
    const [members, setMembers] = useState<ClusterMember[]>([]);
    const [loading, setLoading] = useState(true);
    const [busy, setBusy] = useState('');
    const [error, setError] = useState('');
    const [success, setSuccess] = useState('');
    const [addOpen, setAddOpen] = useState(false);
    const [email, setEmail] = useState('');
    const [permission, setPermission] = useState<'VIEWER' | 'OPERATOR'>('OPERATOR');
    const [editing, setEditing] = useState<ClusterMember | null>(null);
    const [editPermission, setEditPermission] = useState<ClusterRole>('VIEWER');
    const [removeTarget, setRemoveTarget] = useState<ClusterMember | null>(null);

    const load = useCallback(async () => {
        if (!clusterId) {
            setMembers([]);
            return;
        }
        setLoading(true);
        setError('');
        try {
            setMembers(await api.members());
        } catch (loadError) {
            setError(errorMessage(loadError, 'Failed to load cluster members'));
        } finally {
            setLoading(false);
        }
    }, [api, clusterId]);

    useEffect(() => {
        if (ready) void load();
    }, [load, ready]);

    const add = async (event: React.FormEvent) => {
        event.preventDefault();
        setBusy('add');
        setError('');
        try {
            await api.addMemberByEmail({ email: email.trim(), permission });
            setAddOpen(false);
            setEmail('');
            setSuccess('Cluster member added.');
            await load();
        } catch (addError) {
            setError(errorMessage(addError, 'Failed to add cluster member'));
        } finally {
            setBusy('');
        }
    };

    const update = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!editing) return;
        setBusy(editing.user_id);
        setError('');
        try {
            await api.updateMember(editing.user_id, editPermission);
            setEditing(null);
            setSuccess(
                editPermission === 'OWNER'
                    ? 'Cluster ownership transferred.'
                    : 'Member role updated.'
            );
            await load();
        } catch (updateError) {
            setError(errorMessage(updateError, 'Failed to update member role'));
        } finally {
            setBusy('');
        }
    };

    const remove = async () => {
        if (!removeTarget) return;
        setBusy(removeTarget.user_id);
        setError('');
        try {
            await api.removeMember(removeTarget.user_id);
            setRemoveTarget(null);
            setSuccess('Cluster member removed.');
            await load();
        } catch (removeError) {
            setError(errorMessage(removeError, 'Failed to remove cluster member'));
        } finally {
            setBusy('');
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
                            setAddOpen(true);
                        }}
                    >
                        <Plus className='h-4 w-4' /> Add member
                    </Button>
                }
                subtitle='Manage access to the selected cluster.'
                title='Cluster members'
            />
            {error && !addOpen && !editing && <FormError message={error} />}
            {success && (
                <div className='rounded-lg border border-success/20 bg-success/10 px-4 py-3 text-sm text-success'>
                    {success}
                </div>
            )}
            {!clusterId ? (
                <div className='py-12 text-center text-sm text-muted'>
                    Select a cluster to manage members.
                </div>
            ) : (
                <DataTable
                    aria-label='Cluster members'
                    empty={members.length === 0}
                    emptyDescription='Add a member to share access to this cluster.'
                    emptyTitle='No cluster members'
                    loading={loading && members.length === 0}
                    title={`${members.length} members`}
                >
                    <thead>
                        <tr>
                            <th>User</th>
                            <th>Status</th>
                            <th>Cluster role</th>
                            <th>Added</th>
                            <th aria-label='Actions' />
                        </tr>
                    </thead>
                    <tbody>
                        {members.map((member) => {
                            const isOwner = member.permission === 'OWNER';
                            return (
                                <tr key={member.user_id}>
                                    <td>
                                        <div className='text-sm font-medium'>
                                            {member.name || member.email || member.user_id}
                                        </div>
                                        <div className='text-xs text-muted'>
                                            {member.email || member.user_id}
                                        </div>
                                    </td>
                                    <td>
                                        <span
                                            className={
                                                member.status === 'ACTIVE'
                                                    ? 'text-success'
                                                    : 'text-danger'
                                            }
                                        >
                                            {member.status || 'ACTIVE'}
                                        </span>
                                    </td>
                                    <td className='text-xs font-semibold'>{member.permission}</td>
                                    <td className='whitespace-nowrap text-xs text-muted'>
                                        {new Date(member.created_at).toLocaleDateString()}
                                    </td>
                                    <td>
                                        {canManage && !isOwner && (
                                            <div className='flex items-center justify-end gap-1'>
                                                <Button
                                                    isIconOnly
                                                    aria-label={`Edit ${member.name || member.email}`}
                                                    isDisabled={Boolean(busy)}
                                                    size='sm'
                                                    variant='ghost'
                                                    onPress={() => {
                                                        setEditPermission(member.permission);
                                                        setEditing(member);
                                                        setError('');
                                                    }}
                                                >
                                                    <Pencil className='h-4 w-4' />
                                                </Button>
                                                <Button
                                                    isIconOnly
                                                    aria-label={`Remove ${member.name || member.email}`}
                                                    isDisabled={Boolean(busy)}
                                                    size='sm'
                                                    variant='ghost'
                                                    onPress={() => setRemoveTarget(member)}
                                                >
                                                    <Trash2 className='h-4 w-4 text-danger' />
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
                icon={<Users className='h-5 w-5' />}
                isOpen={addOpen}
                size='sm'
                subtitle='Grant access to an active Goveto account.'
                title='Add cluster member'
                onOpenChange={setAddOpen}
            >
                <form onSubmit={add}>
                    <div className='space-y-4 px-6 py-5'>
                        {error && <FormError message={error} />}
                        <FormField htmlFor='member-email' label='Email' required>
                            <Input
                                id='member-email'
                                type='email'
                                variant='secondary'
                                value={email}
                                onChange={(event) => setEmail(event.target.value)}
                            />
                        </FormField>
                        <FormField label='Cluster role' required>
                            <SelectField
                                ariaLabel='Cluster role'
                                options={[
                                    { id: 'VIEWER', label: 'Viewer' },
                                    { id: 'OPERATOR', label: 'Operator' },
                                ]}
                                value={permission}
                                variant='secondary'
                                onChange={(value) => setPermission(value as 'VIEWER' | 'OPERATOR')}
                            />
                        </FormField>
                    </div>
                    <DialogFooter>
                        <Button type='button' variant='ghost' onPress={() => setAddOpen(false)}>
                            Cancel
                        </Button>
                        <Button isDisabled={Boolean(busy) || !email.trim()} type='submit'>
                            {busy === 'add' && <Loader2 className='h-4 w-4 animate-spin' />}
                            Add member
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <DialogShell
                icon={<Pencil className='h-5 w-5' />}
                isOpen={editing !== null}
                size='sm'
                subtitle={editing?.email}
                title='Change member role'
                onOpenChange={(open) => {
                    if (!open) setEditing(null);
                }}
            >
                <form onSubmit={update}>
                    <div className='space-y-4 px-6 py-5'>
                        {error && <FormError message={error} />}
                        <FormField label='Cluster role' required>
                            <SelectField
                                ariaLabel='Cluster role'
                                options={[
                                    { id: 'VIEWER', label: 'Viewer' },
                                    { id: 'OPERATOR', label: 'Operator' },
                                    { id: 'OWNER', label: 'Owner' },
                                ]}
                                value={editPermission}
                                variant='secondary'
                                onChange={(value) => setEditPermission(value as ClusterRole)}
                            />
                        </FormField>
                    </div>
                    <DialogFooter>
                        <Button type='button' variant='ghost' onPress={() => setEditing(null)}>
                            Cancel
                        </Button>
                        <Button isDisabled={Boolean(busy)} type='submit'>
                            {busy && <Loader2 className='h-4 w-4 animate-spin' />}
                            Save
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <ConfirmDialog
                danger
                confirmLabel='Remove member'
                description={
                    removeTarget
                        ? `Remove ${removeTarget.name || removeTarget.email} from this cluster?`
                        : undefined
                }
                isOpen={removeTarget !== null}
                loading={Boolean(busy)}
                title='Remove cluster member'
                onConfirm={() => void remove()}
                onOpenChange={(open) => {
                    if (!open) setRemoveTarget(null);
                }}
            />
        </div>
    );
}
