import type { AlertDelivery, AlertDetail, AlertEvent, AlertInstance, AlertSeverity } from '@/api';

import { Button, Input, Tabs, Tooltip } from '@heroui/react';
import { CheckCheck, CheckCircle2, Eye, RefreshCw, Siren, Timer, XCircle } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, alertsApi } from '@/api';
import { AlertRulesPanel } from '@/components/AlertRulesPanel.tsx';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { StatCard } from '@/components/StatCard.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useAlertOverview } from '@/hooks/useAlertOverview.tsx';
import { useCluster } from '@/hooks/useCluster.ts';
import {
    alertStatusLabels,
    formatAlertAge,
    humanizeAlertKind,
    severityBadgeClass,
    severityLabel,
} from '@/utils/alerts.ts';
import { canOperateCluster } from '@/utils/rbac.ts';

const statusFilters = [
    ['', 'All statuses'],
    ['FIRING', 'Firing'],
    ['ACKNOWLEDGED', 'Acknowledged'],
    ['PENDING', 'Pending'],
    ['RESOLVED', 'Resolved'],
] as const;

const severityFilters = [
    ['', 'All severities'],
    ['CRITICAL', 'Critical'],
    ['WARNING', 'Warning'],
    ['INFO', 'Info'],
] as const;

function formatTime(value?: string | null) {
    if (!value) return '-';
    return new Intl.DateTimeFormat(undefined, {
        month: 'short',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
    }).format(new Date(value));
}

function SeverityBadge({ severity }: { severity: AlertSeverity }) {
    return (
        <span
            className={`inline-flex items-center rounded-md px-2 py-0.5 text-xs font-semibold ${severityBadgeClass(severity)}`}
        >
            {severityLabel(severity)}
        </span>
    );
}

function detailRows(detail: Record<string, unknown>) {
    return Object.entries(detail)
        .filter(([, value]) => value !== null && value !== undefined && String(value) !== '')
        .map(([key, value]) => ({ key, value: String(value) }));
}

function AlertTimeline({ events }: { events: AlertEvent[] }) {
    const eventLabels: Record<string, string> = {
        STATE_CHANGE: 'State change',
        NOTIFY: 'Notification',
        ACK: 'Acknowledged',
        RESOLVE: 'Resolved',
        REARM: 'Rearmed after condition cleared',
    };
    if (events.length === 0) {
        return <p className='text-sm text-muted'>No events recorded.</p>;
    }
    return (
        <ol className='space-y-2'>
            {events.map((event) => (
                <li
                    className='flex items-start gap-3 rounded-lg border border-border bg-surface-secondary px-3 py-2'
                    key={event.id}
                >
                    <div className='min-w-0 flex-1'>
                        <div className='text-xs font-semibold'>
                            {eventLabels[event.type] ?? event.type}
                            {event.type === 'STATE_CHANGE' &&
                                event.fromStatus &&
                                event.toStatus && (
                                    <span className='ml-2 font-normal text-muted'>
                                        {alertStatusLabels[event.fromStatus] ?? event.fromStatus} →{' '}
                                        {alertStatusLabels[event.toStatus] ?? event.toStatus}
                                    </span>
                                )}
                        </div>
                        <div className='mt-0.5 text-xs text-muted'>
                            {formatTime(event.createdAt)}
                        </div>
                    </div>
                </li>
            ))}
        </ol>
    );
}

function DeliveryList({ deliveries }: { deliveries: AlertDelivery[] }) {
    if (deliveries.length === 0) {
        return (
            <p className='text-sm text-muted'>
                No notification deliveries. Bind channels to the rule to receive notifications.
            </p>
        );
    }
    return (
        <div className='overflow-x-auto'>
            <table className='w-full text-sm'>
                <thead>
                    <tr className='border-b border-border text-left text-xs text-muted'>
                        <th className='py-2 pr-4 font-medium'>Channel</th>
                        <th className='py-2 pr-4 font-medium'>Phase</th>
                        <th className='py-2 pr-4 font-medium'>Status</th>
                        <th className='py-2 pr-4 font-medium'>Attempts</th>
                        <th className='py-2 font-medium'>Sent</th>
                    </tr>
                </thead>
                <tbody>
                    {deliveries.map((delivery) => (
                        <tr className='border-b border-border/60' key={delivery.id}>
                            <td className='py-2 pr-4'>
                                {delivery.channelName || delivery.channelId}
                            </td>
                            <td className='py-2 pr-4 text-xs'>
                                {delivery.kind === 'recovery' ? 'Recovery' : 'Firing'}
                            </td>
                            <td className='py-2 pr-4'>
                                <StatusBadge status={delivery.status} />
                                {delivery.lastError && (
                                    <div
                                        className='mt-1 max-w-64 truncate text-xs text-danger'
                                        title={delivery.lastError}
                                    >
                                        {delivery.lastError}
                                    </div>
                                )}
                            </td>
                            <td className='py-2 pr-4 font-mono text-xs'>{delivery.attempts}</td>
                            <td className='py-2 text-xs text-muted'>
                                {formatTime(delivery.sentAt ?? delivery.createdAt)}
                            </td>
                        </tr>
                    ))}
                </tbody>
            </table>
        </div>
    );
}

export default function Alerts() {
    const { clusterId, clusters } = useCluster();
    const { refresh: refreshAlertOverview } = useAlertOverview();
    const role = clusters.find((cluster) => cluster.id === clusterId)?.role;
    const canOperate = canOperateCluster(role);
    const api = useMemo(() => alertsApi(clusterId), [clusterId]);

    const [status, setStatus] = useState<AlertInstance['status'] | ''>('');
    const [severity, setSeverity] = useState<AlertSeverity | ''>('');
    const [activeOnly, setActiveOnly] = useState('true');
    const [page, setPage] = useState(1);
    const [pageSize, setPageSize] = useState(25);
    const [total, setTotal] = useState(0);
    const [counts, setCounts] = useState({ firing: 0, acknowledged: 0, pending: 0 });
    const [items, setItems] = useState<AlertInstance[]>([]);
    const [selected, setSelected] = useState<AlertDetail | null>(null);
    const [events, setEvents] = useState<AlertEvent[]>([]);
    const [eventPage, setEventPage] = useState(1);
    const [eventTotal, setEventTotal] = useState(0);
    const [deliveries, setDeliveries] = useState<AlertDelivery[]>([]);
    const [deliveryPage, setDeliveryPage] = useState(1);
    const [deliveryTotal, setDeliveryTotal] = useState(0);
    const [historyLoading, setHistoryLoading] = useState(false);
    const [detailLoading, setDetailLoading] = useState(false);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [actionError, setActionError] = useState('');
    const [resolveError, setResolveError] = useState('');
    const [mutating, setMutating] = useState(false);
    const [confirmResolve, setConfirmResolve] = useState<AlertInstance | null>(null);
    const [resolveReason, setResolveReason] = useState('');
    const requestVersion = useRef(0);
    const detailVersion = useRef(0);
    const detailController = useRef<AbortController | null>(null);
    const previousClusterID = useRef(clusterId);

    const load = useCallback(async () => {
        if (!clusterId) return;
        const version = ++requestVersion.current;
        setLoading(true);
        try {
            const result = await api.list({
                status: status || undefined,
                severity: severity || undefined,
                active: activeOnly === '' ? undefined : activeOnly === 'true',
                page,
                page_size: pageSize,
            });
            if (version !== requestVersion.current) return;
            const nextPageCount = Math.max(1, Math.ceil(result.total / pageSize));
            if (page > nextPageCount) {
                setPage(nextPageCount);
                return;
            }
            setItems(result.items);
            setTotal(result.total);
            setError('');
        } catch (loadError) {
            if (version !== requestVersion.current) return;
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load alerts');
        } finally {
            if (version === requestVersion.current) setLoading(false);
        }
    }, [api, clusterId, status, severity, activeOnly, page, pageSize]);

    const loadCounts = useCallback(async () => {
        if (!clusterId) return;
        try {
            const [firing, acknowledged, pending] = await Promise.all([
                api.list({ status: 'FIRING', page_size: 1 }),
                api.list({ status: 'ACKNOWLEDGED', page_size: 1 }),
                api.list({ status: 'PENDING', page_size: 1 }),
            ]);
            setCounts({
                firing: firing.total,
                acknowledged: acknowledged.total,
                pending: pending.total,
            });
        } catch {
            // Summary counts are best effort; the table surfaces load errors.
        }
    }, [api, clusterId]);

    useEffect(() => {
        if (previousClusterID.current !== clusterId) {
            previousClusterID.current = clusterId;
            setPage(1);
            setSelected(null);
            detailController.current?.abort();
        }
    }, [clusterId]);

    useEffect(() => {
        void load();
    }, [load]);

    useEffect(() => {
        void loadCounts();
    }, [loadCounts]);

    useEffect(
        () => () => {
            detailController.current?.abort();
            detailVersion.current++;
        },
        []
    );

    const openDetail = useCallback(
        async (alertId: string) => {
            detailController.current?.abort();
            const controller = new AbortController();
            detailController.current = controller;
            const version = ++detailVersion.current;
            setDetailLoading(true);
            setHistoryLoading(true);
            setActionError('');
            setSelected(null);
            setEvents([]);
            setDeliveries([]);
            try {
                const [detail, eventResult, deliveryResult] = await Promise.all([
                    api.detail(alertId, { signal: controller.signal }),
                    api.events(alertId, 1, 50, { signal: controller.signal }),
                    api.deliveries(alertId, 1, 50, { signal: controller.signal }),
                ]);
                if (controller.signal.aborted || version !== detailVersion.current) return;
                setSelected(detail);
                setEvents(eventResult.items);
                setEventPage(eventResult.page);
                setEventTotal(eventResult.total);
                setDeliveries(deliveryResult.items);
                setDeliveryPage(deliveryResult.page);
                setDeliveryTotal(deliveryResult.total);
            } catch (detailError) {
                if (controller.signal.aborted || version !== detailVersion.current) return;
                setActionError(
                    detailError instanceof ApiError ? detailError.message : 'Failed to load alert'
                );
            } finally {
                if (version === detailVersion.current) {
                    setDetailLoading(false);
                    setHistoryLoading(false);
                }
            }
        },
        [api]
    );

    const loadMoreEvents = useCallback(async () => {
        if (!selected || events.length >= eventTotal) return;
        const version = detailVersion.current;
        setHistoryLoading(true);
        try {
            const result = await api.events(selected.id, eventPage + 1, 50, {
                signal: detailController.current?.signal,
            });
            if (version !== detailVersion.current) return;
            setEvents((current) => [...current, ...result.items]);
            setEventPage(result.page);
            setEventTotal(result.total);
        } catch (historyError) {
            if (version !== detailVersion.current) return;
            setActionError(
                historyError instanceof ApiError ? historyError.message : 'Failed to load events'
            );
        } finally {
            if (version === detailVersion.current) setHistoryLoading(false);
        }
    }, [api, eventPage, eventTotal, events.length, selected]);

    const loadMoreDeliveries = useCallback(async () => {
        if (!selected || deliveries.length >= deliveryTotal) return;
        const version = detailVersion.current;
        setHistoryLoading(true);
        try {
            const result = await api.deliveries(selected.id, deliveryPage + 1, 50, {
                signal: detailController.current?.signal,
            });
            if (version !== detailVersion.current) return;
            setDeliveries((current) => [...current, ...result.items]);
            setDeliveryPage(result.page);
            setDeliveryTotal(result.total);
        } catch (historyError) {
            if (version !== detailVersion.current) return;
            setActionError(
                historyError instanceof ApiError
                    ? historyError.message
                    : 'Failed to load deliveries'
            );
        } finally {
            if (version === detailVersion.current) setHistoryLoading(false);
        }
    }, [api, deliveries.length, deliveryPage, deliveryTotal, selected]);

    const acknowledge = useCallback(
        async (alert: AlertInstance) => {
            setMutating(true);
            try {
                await api.ack(alert.id);
                setActionError('');
                await Promise.all([load(), loadCounts(), refreshAlertOverview()]);
            } catch (ackError) {
                setActionError(ackError instanceof ApiError ? ackError.message : 'Ack failed');
            } finally {
                setMutating(false);
            }
        },
        [api, load, loadCounts, refreshAlertOverview]
    );

    const acknowledgeAllFiring = useCallback(async () => {
        const firingIds = items.filter((item) => item.status === 'FIRING').map((item) => item.id);
        if (firingIds.length === 0) return;
        setMutating(true);
        try {
            await api.batchAck(firingIds);
            setActionError('');
            await Promise.all([load(), loadCounts(), refreshAlertOverview()]);
        } catch (batchError) {
            setActionError(
                batchError instanceof ApiError ? batchError.message : 'Batch acknowledge failed'
            );
        } finally {
            setMutating(false);
        }
    }, [api, items, load, loadCounts, refreshAlertOverview]);

    const resolve = useCallback(
        async (alert: AlertInstance, reason: string) => {
            setMutating(true);
            try {
                await api.resolve(alert.id, reason);
                setActionError('');
                setConfirmResolve(null);
                setResolveReason('');
                setResolveError('');
                await Promise.all([load(), loadCounts(), refreshAlertOverview()]);
            } catch (resolveError) {
                setResolveError(
                    resolveError instanceof ApiError ? resolveError.message : 'Resolve failed'
                );
            } finally {
                setMutating(false);
            }
        },
        [api, load, loadCounts, refreshAlertOverview]
    );

    const pageCount = Math.max(1, Math.ceil(total / pageSize));
    const rangeStart = total === 0 ? 0 : (page - 1) * pageSize + 1;
    const rangeEnd = Math.min(page * pageSize, total);
    const firingOnPage = items.filter((item) => item.status === 'FIRING').length;

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Monitor firing alerts and their notifications.'
                    title='Alerts'
                />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to view alerts.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                actions={
                    <Button
                        isIconOnly
                        aria-label='Refresh alerts'
                        variant='ghost'
                        onPress={() => {
                            void load();
                            void loadCounts();
                        }}
                    >
                        <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
                    </Button>
                }
                subtitle='Monitor firing alerts, acknowledge incidents, and tune notification rules.'
                title='Alerts'
            />

            {(error || actionError) && (
                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                    {error || actionError}
                </div>
            )}

            <div className='grid gap-4 sm:grid-cols-3'>
                <StatCard color='danger' icon={Siren} label='Firing' value={counts.firing} />
                <StatCard
                    color='warning'
                    icon={Timer}
                    label='Acknowledged'
                    value={counts.acknowledged}
                />
                <StatCard
                    color='primary'
                    icon={CheckCircle2}
                    label='Pending'
                    value={counts.pending}
                />
            </div>

            <Tabs aria-label='Alert center' defaultSelectedKey='alerts'>
                <Tabs.ListContainer>
                    <Tabs.List aria-label='Alert views'>
                        <Tabs.Tab id='alerts'>
                            Alerts
                            <Tabs.Indicator />
                        </Tabs.Tab>
                        <Tabs.Tab id='rules'>
                            Rules
                            <Tabs.Indicator />
                        </Tabs.Tab>
                    </Tabs.List>
                </Tabs.ListContainer>
                <Tabs.Panel id='alerts'>
                    <div>
                        <DataTable
                            action={
                                <div className='grid w-full gap-2 sm:grid-cols-2 lg:grid-cols-[160px_160px_150px_110px_auto]'>
                                    <SelectField
                                        ariaLabel='Alert status'
                                        options={statusFilters.map(([value, label]) => ({
                                            id: value,
                                            label,
                                        }))}
                                        value={status}
                                        variant='secondary'
                                        onChange={(value) => {
                                            setStatus(value as AlertInstance['status'] | '');
                                            setPage(1);
                                        }}
                                    />
                                    <SelectField
                                        ariaLabel='Alert severity'
                                        options={severityFilters.map(([value, label]) => ({
                                            id: value,
                                            label,
                                        }))}
                                        value={severity}
                                        variant='secondary'
                                        onChange={(value) => {
                                            setSeverity(value as AlertSeverity | '');
                                            setPage(1);
                                        }}
                                    />
                                    <SelectField
                                        ariaLabel='Active filter'
                                        options={[
                                            { id: 'true', label: 'Active only' },
                                            { id: 'false', label: 'Resolved only' },
                                            { id: '', label: 'All' },
                                        ]}
                                        value={activeOnly}
                                        variant='secondary'
                                        onChange={(value) => {
                                            setActiveOnly(value);
                                            setPage(1);
                                        }}
                                    />
                                    <SelectField
                                        ariaLabel='Rows per page'
                                        options={[25, 50, 100].map((value) => ({
                                            id: String(value),
                                            label: `${value} rows`,
                                        }))}
                                        value={String(pageSize)}
                                        variant='secondary'
                                        onChange={(value) => {
                                            setPageSize(Number(value));
                                            setPage(1);
                                        }}
                                    />
                                    {canOperate && firingOnPage > 0 && (
                                        <Tooltip>
                                            <Tooltip.Trigger>
                                                <Button
                                                    isDisabled={mutating}
                                                    size='sm'
                                                    variant='secondary'
                                                    onPress={() => void acknowledgeAllFiring()}
                                                >
                                                    <CheckCheck className='h-4 w-4' />
                                                    Ack all firing ({firingOnPage})
                                                </Button>
                                            </Tooltip.Trigger>
                                            <Tooltip.Content>
                                                Acknowledge every firing alert on this page
                                            </Tooltip.Content>
                                        </Tooltip>
                                    )}
                                </div>
                            }
                            aria-label='Alert instances'
                            className='[&_td]:py-3 [&_th]:tracking-normal'
                            empty={items.length === 0}
                            emptyDescription='Alerts matching the selected filters will appear here.'
                            emptyTitle='No alerts'
                            loading={loading && items.length === 0}
                            title={`${total.toLocaleString()} alerts`}
                        >
                            <thead>
                                <tr>
                                    <th>Severity</th>
                                    <th>Alert</th>
                                    <th>Rule</th>
                                    <th>Status</th>
                                    <th>First seen</th>
                                    <th>Last seen</th>
                                    <th aria-label='Actions' />
                                </tr>
                            </thead>
                            <tbody>
                                {items.map((alert) => {
                                    const ackable = alert.status === 'FIRING';
                                    const resolvable = alert.status !== 'RESOLVED';
                                    return (
                                        <tr key={alert.id}>
                                            <td>
                                                <SeverityBadge severity={alert.severity} />
                                            </td>
                                            <td className='max-w-96'>
                                                <button
                                                    className='block max-w-full text-left hover:text-accent'
                                                    type='button'
                                                    onClick={() => void openDetail(alert.id)}
                                                >
                                                    <span className='block truncate text-sm font-medium'>
                                                        {alert.title}
                                                    </span>
                                                </button>
                                            </td>
                                            <td className='text-xs text-muted'>
                                                {humanizeAlertKind(alert.kind)}
                                            </td>
                                            <td>
                                                <StatusBadge status={alert.status} />
                                                {alert.notifyCount > 0 && (
                                                    <div className='mt-1 text-xs text-muted'>
                                                        {alert.notifyCount} notification
                                                        {alert.notifyCount === 1 ? '' : 's'}
                                                    </div>
                                                )}
                                            </td>
                                            <td className='whitespace-nowrap text-xs text-muted'>
                                                {formatTime(alert.firstSeenAt)}
                                                <div className='text-xs text-muted'>
                                                    {formatAlertAge(alert.firstSeenAt)}
                                                </div>
                                            </td>
                                            <td className='whitespace-nowrap text-xs text-muted'>
                                                {formatTime(alert.lastSeenAt)}
                                            </td>
                                            <td>
                                                <div className='flex justify-end gap-1'>
                                                    <Tooltip>
                                                        <Tooltip.Trigger>
                                                            <Button
                                                                isIconOnly
                                                                aria-label='View alert details'
                                                                size='sm'
                                                                variant='ghost'
                                                                onPress={() =>
                                                                    void openDetail(alert.id)
                                                                }
                                                            >
                                                                <Eye className='h-4 w-4' />
                                                            </Button>
                                                        </Tooltip.Trigger>
                                                        <Tooltip.Content>Details</Tooltip.Content>
                                                    </Tooltip>
                                                    {canOperate && ackable && (
                                                        <Tooltip>
                                                            <Tooltip.Trigger>
                                                                <Button
                                                                    isDisabled={mutating}
                                                                    isIconOnly
                                                                    aria-label='Acknowledge alert'
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() =>
                                                                        void acknowledge(alert)
                                                                    }
                                                                >
                                                                    <CheckCircle2 className='h-4 w-4' />
                                                                </Button>
                                                            </Tooltip.Trigger>
                                                            <Tooltip.Content>
                                                                Acknowledge
                                                            </Tooltip.Content>
                                                        </Tooltip>
                                                    )}
                                                    {canOperate && resolvable && (
                                                        <Tooltip>
                                                            <Tooltip.Trigger>
                                                                <Button
                                                                    isDisabled={mutating}
                                                                    isIconOnly
                                                                    aria-label='Resolve alert'
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() => {
                                                                        setResolveReason('');
                                                                        setResolveError('');
                                                                        setConfirmResolve(alert);
                                                                    }}
                                                                >
                                                                    <XCircle className='h-4 w-4' />
                                                                </Button>
                                                            </Tooltip.Trigger>
                                                            <Tooltip.Content>
                                                                Resolve
                                                            </Tooltip.Content>
                                                        </Tooltip>
                                                    )}
                                                </div>
                                            </td>
                                        </tr>
                                    );
                                })}
                            </tbody>
                        </DataTable>

                        {total > 0 && (
                            <div className='flex items-center justify-between pt-2 text-xs text-muted'>
                                <span>
                                    Showing {rangeStart.toLocaleString()}-
                                    {rangeEnd.toLocaleString()} of {total.toLocaleString()}
                                </span>
                                <div className='flex gap-2'>
                                    <Button
                                        isDisabled={page <= 1}
                                        size='sm'
                                        variant='secondary'
                                        onPress={() =>
                                            setPage((current) => Math.max(1, current - 1))
                                        }
                                    >
                                        Previous
                                    </Button>
                                    <span className='flex items-center px-1'>
                                        {page} / {pageCount}
                                    </span>
                                    <Button
                                        isDisabled={page >= pageCount}
                                        size='sm'
                                        variant='secondary'
                                        onPress={() =>
                                            setPage((current) => Math.min(pageCount, current + 1))
                                        }
                                    >
                                        Next
                                    </Button>
                                </div>
                            </div>
                        )}
                    </div>
                </Tabs.Panel>
                <Tabs.Panel id='rules'>
                    <div>
                        <AlertRulesPanel
                            canOperate={canOperate}
                            onChanged={async () => {
                                await Promise.all([loadCounts(), refreshAlertOverview()]);
                            }}
                        />
                    </div>
                </Tabs.Panel>
            </Tabs>

            <DialogShell
                isOpen={Boolean(selected) || detailLoading}
                isDismissable={!detailLoading}
                onOpenChange={(open) => {
                    if (!open) {
                        detailController.current?.abort();
                        detailVersion.current++;
                        setSelected(null);
                    }
                }}
                size='lg'
                subtitle={selected ? humanizeAlertKind(selected.kind) : 'Loading alert…'}
                title={selected?.title ?? 'Alert'}
            >
                {detailLoading || !selected ? (
                    <div className='h-40 animate-pulse rounded-lg bg-surface-secondary' />
                ) : (
                    <div className='max-h-[76vh] space-y-6 overflow-y-auto px-6 py-6'>
                        <dl className='grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-3'>
                            <div className='min-w-0'>
                                <dt className='text-xs text-muted'>Status</dt>
                                <dd className='mt-1'>
                                    <StatusBadge status={selected.status} />
                                </dd>
                            </div>
                            <div className='min-w-0'>
                                <dt className='text-xs text-muted'>Severity</dt>
                                <dd className='mt-1'>
                                    <SeverityBadge severity={selected.severity} />
                                </dd>
                            </div>
                            <div className='min-w-0'>
                                <dt className='text-xs text-muted'>Notifications</dt>
                                <dd className='mt-1 text-sm font-mono'>{selected.notifyCount}</dd>
                            </div>
                            <div className='min-w-0'>
                                <dt className='text-xs text-muted'>First seen</dt>
                                <dd className='mt-1 text-sm'>{formatTime(selected.firstSeenAt)}</dd>
                            </div>
                            <div className='min-w-0'>
                                <dt className='text-xs text-muted'>Last seen</dt>
                                <dd className='mt-1 text-sm'>{formatTime(selected.lastSeenAt)}</dd>
                            </div>
                            <div className='min-w-0'>
                                <dt className='text-xs text-muted'>Resolved</dt>
                                <dd className='mt-1 text-sm'>
                                    {formatTime(selected.resolvedAt)}
                                    {selected.resolvedReason ? ` · ${selected.resolvedReason}` : ''}
                                </dd>
                            </div>
                        </dl>
                        {detailRows(selected.detailJson ?? {}).length > 0 && (
                            <section>
                                <h3 className='border-b border-border pb-2 text-sm font-semibold'>
                                    Detail
                                </h3>
                                <dl className='grid gap-x-6 gap-y-3 pt-3 sm:grid-cols-2 lg:grid-cols-3'>
                                    {detailRows(selected.detailJson ?? {}).map((row) => (
                                        <div className='min-w-0' key={row.key}>
                                            <dt className='text-xs text-muted'>{row.key}</dt>
                                            <dd
                                                className='mt-1 break-words font-mono text-xs'
                                                title={row.value}
                                            >
                                                {row.value}
                                            </dd>
                                        </div>
                                    ))}
                                </dl>
                            </section>
                        )}
                        <section>
                            <h3 className='border-b border-border pb-2 text-sm font-semibold'>
                                Timeline
                            </h3>
                            <div className='pt-3'>
                                <AlertTimeline events={events} />
                                {events.length < eventTotal && (
                                    <Button
                                        className='mt-3'
                                        isDisabled={historyLoading}
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => void loadMoreEvents()}
                                    >
                                        Load more events
                                    </Button>
                                )}
                            </div>
                        </section>
                        <section>
                            <h3 className='border-b border-border pb-2 text-sm font-semibold'>
                                Notification deliveries
                            </h3>
                            <div className='pt-3'>
                                <DeliveryList deliveries={deliveries} />
                                {deliveries.length < deliveryTotal && (
                                    <Button
                                        className='mt-3'
                                        isDisabled={historyLoading}
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => void loadMoreDeliveries()}
                                    >
                                        Load more deliveries
                                    </Button>
                                )}
                            </div>
                        </section>
                    </div>
                )}
            </DialogShell>

            <DialogShell
                isOpen={Boolean(confirmResolve)}
                onOpenChange={(open) => {
                    if (!open) {
                        setConfirmResolve(null);
                        setResolveError('');
                    }
                }}
                size='md'
                subtitle='The alert stays suppressed while this condition persists and can fire again only after the condition clears.'
                title='Resolve alert'
            >
                <div className='space-y-4 px-6 py-6'>
                    <p className='text-sm'>{confirmResolve?.title}</p>
                    <FormField label='Reason (optional)' hint='Recorded in the alert timeline.'>
                        <Input
                            aria-label='Resolve reason'
                            value={resolveReason}
                            variant='secondary'
                            onChange={(event) => setResolveReason(event.target.value)}
                        />
                    </FormField>
                    {resolveError && <FormError message={resolveError} />}
                </div>
                <DialogFooter>
                    <Button
                        variant='ghost'
                        onPress={() => {
                            setConfirmResolve(null);
                            setResolveError('');
                        }}
                    >
                        Cancel
                    </Button>
                    <Button
                        isDisabled={mutating}
                        variant='primary'
                        onPress={() => {
                            if (confirmResolve) void resolve(confirmResolve, resolveReason.trim());
                        }}
                    >
                        <CheckCircle2 className='h-4 w-4' />
                        Resolve
                    </Button>
                </DialogFooter>
            </DialogShell>
        </div>
    );
}
