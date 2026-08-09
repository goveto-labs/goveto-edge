import type { PlatformUser, UserStatus } from '@/api';

import { Button, Input, Pagination } from '@heroui/react';
import { RefreshCw, ShieldCheck, ShieldOff } from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { Navigate } from 'react-router-dom';

import { ApiError, usersApi } from '@/api';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { FormError } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { useAuth } from '@/hooks/useAuth.ts';

function errorMessage(error: unknown) {
    return error instanceof ApiError || error instanceof Error
        ? error.message
        : 'Failed to load users';
}

export default function Users({ embedded = false }: { embedded?: boolean }) {
    const { user: currentUser } = useAuth();
    const [items, setItems] = useState<PlatformUser[]>([]);
    const [total, setTotal] = useState(0);
    const [page, setPage] = useState(1);
    const [search, setSearch] = useState('');
    const [role, setRole] = useState('');
    const [status, setStatus] = useState('');
    const [loading, setLoading] = useState(false);
    const [busy, setBusy] = useState('');
    const [error, setError] = useState('');
    const [target, setTarget] = useState<PlatformUser | null>(null);
    const pageSize = 50;

    const load = useCallback(async () => {
        if (currentUser?.role !== 'ADMIN') return;
        setLoading(true);
        setError('');
        try {
            const response = await usersApi.list({
                search: search.trim() || undefined,
                role: role || undefined,
                status: status || undefined,
                page,
                page_size: pageSize,
            });
            setItems(response.items ?? []);
            setTotal(response.total);
        } catch (loadError) {
            setError(errorMessage(loadError));
        } finally {
            setLoading(false);
        }
    }, [currentUser?.role, page, role, search, status]);

    useEffect(() => {
        const timeout = window.setTimeout(() => void load(), 250);
        return () => window.clearTimeout(timeout);
    }, [load]);

    if (currentUser?.role !== 'ADMIN') return <Navigate replace to='/' />;

    const updateStatus = async () => {
        if (!target) return;
        const nextStatus: UserStatus = target.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE';
        setBusy(target.id);
        setError('');
        try {
            await usersApi.updateStatus(target.id, nextStatus);
            setTarget(null);
            await load();
        } catch (updateError) {
            setError(errorMessage(updateError));
        } finally {
            setBusy('');
        }
    };

    const pageCount = Math.max(1, Math.ceil(total / pageSize));
    const rangeStart = total === 0 ? 0 : (page - 1) * pageSize + 1;
    const rangeEnd = Math.min(page * pageSize, total);

    return (
        <div className='space-y-5'>
            <PageHeader
                actions={
                    <Button isIconOnly aria-label='Refresh users' variant='ghost' onPress={load}>
                        <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
                    </Button>
                }
                embedded={embedded}
                subtitle='Review accounts and control access to this Goveto instance.'
                title='Users'
            />
            {error && <FormError message={error} />}
            <DataTable
                aria-label='Platform users'
                action={
                    <div className='grid w-full gap-2 sm:grid-cols-3'>
                        <Input
                            aria-label='Search users'
                            placeholder='Search name or email'
                            value={search}
                            variant='secondary'
                            onChange={(event) => {
                                setSearch(event.target.value);
                                setPage(1);
                            }}
                        />
                        <SelectField
                            ariaLabel='Role'
                            options={[
                                { id: '', label: 'All roles' },
                                { id: 'ADMIN', label: 'Admin' },
                                { id: 'OPERATOR', label: 'Operator' },
                                { id: 'VIEWER', label: 'Viewer' },
                            ]}
                            value={role}
                            variant='secondary'
                            onChange={(value) => {
                                setRole(value);
                                setPage(1);
                            }}
                        />
                        <SelectField
                            ariaLabel='Status'
                            options={[
                                { id: '', label: 'All statuses' },
                                { id: 'ACTIVE', label: 'Active' },
                                { id: 'DISABLED', label: 'Disabled' },
                            ]}
                            value={status}
                            variant='secondary'
                            onChange={(value) => {
                                setStatus(value);
                                setPage(1);
                            }}
                        />
                    </div>
                }
                empty={items.length === 0}
                emptyDescription='Users matching the selected filters will appear here.'
                emptyTitle='No users found'
                loading={loading && items.length === 0}
                title={`${total.toLocaleString()} users`}
            >
                <thead>
                    <tr>
                        <th>User</th>
                        <th>Role</th>
                        <th>Status</th>
                        <th>2FA</th>
                        <th>Last login</th>
                        <th aria-label='Actions' />
                    </tr>
                </thead>
                <tbody>
                    {items.map((item) => (
                        <tr key={item.id}>
                            <td>
                                <div className='text-sm font-medium'>{item.name || item.email}</div>
                                <div className='text-xs text-muted'>{item.email}</div>
                            </td>
                            <td className='text-xs font-semibold'>{item.role}</td>
                            <td>
                                <span
                                    className={
                                        item.status === 'ACTIVE' ? 'text-success' : 'text-danger'
                                    }
                                >
                                    {item.status}
                                </span>
                            </td>
                            <td className='text-xs'>
                                {item.totp_enabled ? 'Enabled' : 'Not enabled'}
                            </td>
                            <td className='whitespace-nowrap text-xs text-muted'>
                                {item.last_login_at
                                    ? new Date(item.last_login_at).toLocaleString()
                                    : 'Never'}
                            </td>
                            <td>
                                <Button
                                    isIconOnly
                                    aria-label={
                                        item.status === 'ACTIVE'
                                            ? `Disable ${item.name}`
                                            : `Enable ${item.name}`
                                    }
                                    isDisabled={Boolean(busy) || item.id === currentUser.id}
                                    size='sm'
                                    variant='ghost'
                                    onPress={() => setTarget(item)}
                                >
                                    {item.status === 'ACTIVE' ? (
                                        <ShieldOff className='h-4 w-4 text-danger' />
                                    ) : (
                                        <ShieldCheck className='h-4 w-4 text-success' />
                                    )}
                                </Button>
                            </td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
            {total > 0 && (
                <Pagination className='justify-between' size='sm'>
                    <Pagination.Summary>
                        Showing {rangeStart}-{rangeEnd} of {total.toLocaleString()}
                    </Pagination.Summary>
                    <Pagination.Content>
                        <Pagination.Item>
                            <Pagination.Previous
                                isDisabled={page <= 1}
                                onPress={() => setPage((value) => Math.max(1, value - 1))}
                            >
                                <Pagination.PreviousIcon /> Previous
                            </Pagination.Previous>
                        </Pagination.Item>
                        <Pagination.Item>
                            <Pagination.Link isActive>{page}</Pagination.Link>
                        </Pagination.Item>
                        <Pagination.Item>
                            <Pagination.Next
                                isDisabled={page >= pageCount}
                                onPress={() => setPage((value) => Math.min(pageCount, value + 1))}
                            >
                                Next <Pagination.NextIcon />
                            </Pagination.Next>
                        </Pagination.Item>
                    </Pagination.Content>
                </Pagination>
            )}
            <ConfirmDialog
                confirmLabel={target?.status === 'ACTIVE' ? 'Disable user' : 'Enable user'}
                danger={target?.status === 'ACTIVE'}
                description={
                    target?.status === 'ACTIVE'
                        ? `Disable ${target.name || target.email} and revoke all active sessions?`
                        : `Restore access for ${target?.name || target?.email}?`
                }
                isOpen={target !== null}
                loading={Boolean(busy)}
                title={target?.status === 'ACTIVE' ? 'Disable user' : 'Enable user'}
                onConfirm={() => void updateStatus()}
                onOpenChange={(open) => {
                    if (!open) setTarget(null);
                }}
            />
        </div>
    );
}
