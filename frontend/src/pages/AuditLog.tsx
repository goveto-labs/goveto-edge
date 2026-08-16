import type { AuditEvent } from '@/api';

import { Button, Input, Pagination } from '@heroui/react';
import { Eye, FileClock, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { Navigate } from 'react-router-dom';

import { ApiError, auditApi } from '@/api';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError } from '@/components/FormField.tsx';
import { JsonDiff } from '@/components/JsonDiff.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { useAuth } from '@/hooks/useAuth.ts';

function errorMessage(error: unknown) {
    return error instanceof ApiError || error instanceof Error
        ? error.message
        : 'Failed to load audit events';
}

function formatTime(value: string) {
    return new Date(value).toLocaleString();
}

export default function AuditLog({ embedded = false }: { embedded?: boolean }) {
    const { user } = useAuth();
    const [items, setItems] = useState<AuditEvent[]>([]);
    const [total, setTotal] = useState(0);
    const [page, setPage] = useState(1);
    const [actor, setActor] = useState('');
    const [action, setAction] = useState('');
    const [resourceType, setResourceType] = useState('');
    const [result, setResult] = useState('');
    const [from, setFrom] = useState('');
    const [to, setTo] = useState('');
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [selected, setSelected] = useState<AuditEvent | null>(null);
    const pageSize = 50;

    const load = useCallback(async () => {
        if (user?.role !== 'ADMIN') return;
        setLoading(true);
        setError('');
        try {
            const response = await auditApi.list({
                actor: actor.trim() || undefined,
                action: action.trim() || undefined,
                resource_type: resourceType.trim() || undefined,
                result: result || undefined,
                from: from || undefined,
                to: to || undefined,
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
    }, [action, actor, from, page, resourceType, result, to, user?.role]);

    useEffect(() => {
        setLoading(true);
        const timeout = window.setTimeout(() => void load(), 250);
        return () => window.clearTimeout(timeout);
    }, [load]);

    if (user?.role !== 'ADMIN') return <Navigate replace to='/' />;

    const pageCount = Math.max(1, Math.ceil(total / pageSize));
    const rangeStart = total === 0 ? 0 : (page - 1) * pageSize + 1;
    const rangeEnd = Math.min(page * pageSize, total);

    return (
        <div className='space-y-5'>
            <PageHeader
                actions={
                    <Button
                        isIconOnly
                        aria-label='Refresh audit log'
                        variant='ghost'
                        onPress={load}
                    >
                        <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
                    </Button>
                }
                embedded={embedded}
                subtitle='Review security-relevant control-plane activity.'
                title='Audit log'
            />
            {error && <FormError message={error} />}
            <DataTable
                aria-label='Audit events'
                action={
                    <div className='grid w-full gap-2 md:grid-cols-2 xl:grid-cols-6'>
                        <Input
                            aria-label='Filter by actor'
                            placeholder='Actor'
                            value={actor}
                            variant='secondary'
                            onChange={(event) => {
                                setActor(event.target.value);
                                setPage(1);
                            }}
                        />
                        <Input
                            aria-label='Filter by action'
                            placeholder='Action'
                            value={action}
                            variant='secondary'
                            onChange={(event) => {
                                setAction(event.target.value);
                                setPage(1);
                            }}
                        />
                        <Input
                            aria-label='Filter by resource type'
                            placeholder='Resource type'
                            value={resourceType}
                            variant='secondary'
                            onChange={(event) => {
                                setResourceType(event.target.value);
                                setPage(1);
                            }}
                        />
                        <SelectField
                            ariaLabel='Result'
                            options={[
                                { id: '', label: 'All results' },
                                { id: 'SUCCESS', label: 'Success' },
                                { id: 'FAILURE', label: 'Failure' },
                            ]}
                            value={result}
                            variant='secondary'
                            onChange={(value) => {
                                setResult(value);
                                setPage(1);
                            }}
                        />
                        <Input
                            aria-label='From date'
                            type='date'
                            value={from}
                            variant='secondary'
                            onChange={(event) => {
                                setFrom(event.target.value);
                                setPage(1);
                            }}
                        />
                        <Input
                            aria-label='To date'
                            type='date'
                            value={to}
                            variant='secondary'
                            onChange={(event) => {
                                setTo(event.target.value);
                                setPage(1);
                            }}
                        />
                    </div>
                }
                empty={items.length === 0}
                emptyDescription='Events matching the selected filters will appear here.'
                emptyTitle='No audit events found'
                loading={loading && items.length === 0}
                title={`${total.toLocaleString()} events`}
            >
                <thead>
                    <tr>
                        <th>Time</th>
                        <th>Actor</th>
                        <th>Action</th>
                        <th>Resource</th>
                        <th>Result</th>
                        <th aria-label='Actions' />
                    </tr>
                </thead>
                <tbody>
                    {items.map((event) => (
                        <tr key={event.id}>
                            <td className='whitespace-nowrap text-xs text-muted'>
                                {formatTime(event.created_at)}
                            </td>
                            <td className='max-w-64'>
                                <div className='truncate text-sm font-medium'>
                                    {event.user?.name || event.actor}
                                </div>
                                <div className='truncate text-xs text-muted'>
                                    {event.user?.email || event.source_ip}
                                </div>
                            </td>
                            <td className='font-mono text-xs'>{event.action}</td>
                            <td className='max-w-72'>
                                <div className='truncate text-xs font-medium'>
                                    {event.resource_type}
                                </div>
                                <div className='truncate font-mono text-xs text-muted'>
                                    {event.resource_id || '—'}
                                </div>
                            </td>
                            <td>
                                <span
                                    className={`text-xs font-medium ${event.result === 'SUCCESS' ? 'text-success' : 'text-danger'}`}
                                >
                                    {event.result}
                                </span>
                            </td>
                            <td>
                                <Button
                                    isIconOnly
                                    aria-label='View audit event'
                                    size='sm'
                                    variant='ghost'
                                    onPress={() => setSelected(event)}
                                >
                                    <Eye className='h-4 w-4' />
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
                                onPress={() => setPage((current) => Math.max(1, current - 1))}
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
                                onPress={() =>
                                    setPage((current) => Math.min(pageCount, current + 1))
                                }
                            >
                                Next <Pagination.NextIcon />
                            </Pagination.Next>
                        </Pagination.Item>
                    </Pagination.Content>
                </Pagination>
            )}
            <DialogShell
                icon={<FileClock className='h-5 w-5' />}
                isOpen={selected !== null}
                size='xl'
                subtitle={selected?.request_id || selected?.id}
                title={selected?.action || 'Audit event'}
                onOpenChange={(open) => {
                    if (!open) setSelected(null);
                }}
            >
                {selected && (
                    <div className='space-y-4 p-6'>
                        {selected.failure_reason && <FormError message={selected.failure_reason} />}
                        <div className='grid gap-3 text-xs sm:grid-cols-2 lg:grid-cols-3'>
                            <div>
                                <span className='text-muted'>Actor:</span> {selected.actor}
                            </div>
                            <div>
                                <span className='text-muted'>Source IP:</span> {selected.source_ip}
                            </div>
                            <div>
                                <span className='text-muted'>Resource:</span>{' '}
                                {selected.resource_type}:{selected.resource_id}
                            </div>
                            <div>
                                <span className='text-muted'>Result:</span>{' '}
                                <span
                                    className={
                                        selected.result === 'SUCCESS'
                                            ? 'text-success'
                                            : 'text-danger'
                                    }
                                >
                                    {selected.result}
                                </span>
                            </div>
                            <div>
                                <span className='text-muted'>Created:</span>{' '}
                                {formatTime(selected.created_at)}
                            </div>
                        </div>
                        <JsonDiff after={selected.after} before={selected.before} />
                    </div>
                )}
                <DialogFooter>
                    <Button variant='primary' onPress={() => setSelected(null)}>
                        Close
                    </Button>
                </DialogFooter>
            </DialogShell>
        </div>
    );
}
