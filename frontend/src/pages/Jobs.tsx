import type { JobExecution, ManagedJob, ManagedJobKind } from '@/api';

import { Button, Tooltip } from '@heroui/react';
import { History, RefreshCw, RotateCcw, XCircle } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, jobsApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { canManageCluster, canOperateCluster } from '@/utils/rbac.ts';

const kinds: Array<{ value: '' | ManagedJobKind; label: string }> = [
    { value: '', label: 'All types' },
    { value: 'PUBLISH', label: 'Publish' },
    { value: 'PURGE', label: 'Cache refresh' },
    { value: 'INSTALL', label: 'Installation' },
    { value: 'DNS', label: 'DNS' },
    { value: 'CERTIFICATE', label: 'Certificate' },
];

const statuses = [
    ['', 'All statuses'],
    ['PENDING', 'Pending'],
    ['RUNNING', 'Running'],
    ['SUCCEEDED', 'Succeeded'],
    ['FAILED', 'Failed'],
    ['DEAD_LETTER', 'Dead letter'],
    ['CANCELLED', 'Cancelled'],
] as const;

function formatTime(value?: string) {
    if (!value) return '-';
    return new Intl.DateTimeFormat(undefined, {
        month: 'short',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
    }).format(new Date(value));
}

function shortID(value: string) {
    return value.length > 18 ? `${value.slice(0, 8)}...${value.slice(-6)}` : value;
}

export default function Jobs() {
    const { clusterId, clusters } = useCluster();
    const role = clusters.find((cluster) => cluster.id === clusterId)?.role;
    const canOperate = canOperateCluster(role);
    const canManage = canManageCluster(role);
    const api = useMemo(() => jobsApi(clusterId), [clusterId]);
    const [kind, setKind] = useState<'' | ManagedJobKind>('');
    const [status, setStatus] = useState('');
    const [jobs, setJobs] = useState<ManagedJob[]>([]);
    const [selected, setSelected] = useState<ManagedJob | null>(null);
    const [executions, setExecutions] = useState<JobExecution[]>([]);
    const [loading, setLoading] = useState(false);
    const [historyLoading, setHistoryLoading] = useState(false);
    const [mutating, setMutating] = useState('');
    const [loadError, setLoadError] = useState('');
    const [actionError, setActionError] = useState('');
    const listRequestVersion = useRef(0);
    const historyRequestVersion = useRef(0);
    const mutationRequestVersion = useRef(0);
    const activeClusterID = useRef(clusterId);
    const previousClusterID = useRef(clusterId);
    activeClusterID.current = clusterId;

    const load = useCallback(async () => {
        if (!clusterId) return;
        const version = ++listRequestVersion.current;
        const requestedClusterID = clusterId;
        setLoading(true);
        try {
            const result = await api.list({ kind, status });
            if (
                version !== listRequestVersion.current ||
                requestedClusterID !== activeClusterID.current
            )
                return;
            setJobs(result ?? []);
            setLoadError('');
        } catch (loadError) {
            if (
                version !== listRequestVersion.current ||
                requestedClusterID !== activeClusterID.current
            )
                return;
            setLoadError(loadError instanceof ApiError ? loadError.message : 'Failed to load jobs');
        } finally {
            if (
                version === listRequestVersion.current &&
                requestedClusterID === activeClusterID.current
            )
                setLoading(false);
        }
    }, [api, clusterId, kind, status]);

    useEffect(() => {
        if (previousClusterID.current === clusterId) return;
        previousClusterID.current = clusterId;
        listRequestVersion.current++;
        historyRequestVersion.current++;
        mutationRequestVersion.current++;
        setJobs([]);
        setSelected(null);
        setExecutions([]);
        setLoading(false);
        setHistoryLoading(false);
        setLoadError('');
        setActionError('');
    }, [clusterId]);

    useAutoRefresh(load, Boolean(clusterId));

    const loadHistory = async (job: ManagedJob) => {
        const version = ++historyRequestVersion.current;
        const requestedClusterID = clusterId;
        setSelected(job);
        setExecutions([]);
        setHistoryLoading(true);
        try {
            const result = await api.executions(job.kind, job.id);
            if (
                version !== historyRequestVersion.current ||
                requestedClusterID !== activeClusterID.current
            )
                return;
            setExecutions(result ?? []);
            setLoadError('');
        } catch (loadError) {
            if (
                version !== historyRequestVersion.current ||
                requestedClusterID !== activeClusterID.current
            )
                return;
            setLoadError(
                loadError instanceof ApiError ? loadError.message : 'Failed to load job history'
            );
        } finally {
            if (
                version === historyRequestVersion.current &&
                requestedClusterID === activeClusterID.current
            )
                setHistoryLoading(false);
        }
    };

    const mutate = async (job: ManagedJob, action: 'cancel' | 'replay') => {
        const version = ++mutationRequestVersion.current;
        const requestedClusterID = clusterId;
        setMutating(`${action}:${job.id}`);
        setActionError('');
        try {
            await api[action](job.kind, job.id);
            if (
                version !== mutationRequestVersion.current ||
                requestedClusterID !== activeClusterID.current
            )
                return;
            await load();
            if (
                version !== mutationRequestVersion.current ||
                requestedClusterID !== activeClusterID.current
            )
                return;
            if (selected?.id === job.id) await loadHistory(job);
        } catch (mutationError) {
            if (
                version !== mutationRequestVersion.current ||
                requestedClusterID !== activeClusterID.current
            )
                return;
            setActionError(
                mutationError instanceof ApiError
                    ? mutationError.message
                    : `Failed to ${action} job`
            );
        } finally {
            if (
                version === mutationRequestVersion.current &&
                requestedClusterID === activeClusterID.current
            )
                setMutating('');
        }
    };

    const mayMutate = (job: ManagedJob) =>
        job.kind === 'PUBLISH' || job.kind === 'PURGE' ? canOperate : canManage;

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='Inspect and recover background work.' title='Jobs' />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to view jobs.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                actions={
                    <Button isIconOnly aria-label='Refresh jobs' variant='ghost' onPress={load}>
                        <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
                    </Button>
                }
                subtitle='Inspect leases, execution attempts, partial results, and dead letters.'
                title='Jobs'
            />

            {(actionError || loadError) && (
                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                    {actionError || loadError}
                </div>
            )}

            <DataTable
                aria-label='Background jobs'
                action={
                    <div className='grid w-full grid-cols-2 gap-2 sm:flex sm:w-auto'>
                        <select
                            aria-label='Job type'
                            className='min-h-9 rounded-lg border border-border bg-surface px-3 text-sm'
                            value={kind}
                            onChange={(event) => setKind(event.target.value as '' | ManagedJobKind)}
                        >
                            {kinds.map((item) => (
                                <option key={item.value || 'all'} value={item.value}>
                                    {item.label}
                                </option>
                            ))}
                        </select>
                        <select
                            aria-label='Job status'
                            className='min-h-9 rounded-lg border border-border bg-surface px-3 text-sm'
                            value={status}
                            onChange={(event) => setStatus(event.target.value)}
                        >
                            {statuses.map(([value, label]) => (
                                <option key={value || 'all'} value={value}>
                                    {label}
                                </option>
                            ))}
                        </select>
                    </div>
                }
                empty={jobs.length === 0}
                emptyDescription='Jobs matching the selected filters will appear here.'
                emptyTitle='No jobs found'
                loading={loading && jobs.length === 0}
                title={`${jobs.length} jobs`}
            >
                <thead>
                    <tr>
                        <th>Type</th>
                        <th>Resource</th>
                        <th>Status</th>
                        <th>Attempt</th>
                        <th>Worker lease</th>
                        <th>Updated</th>
                        <th aria-label='Actions' />
                    </tr>
                </thead>
                <tbody>
                    {jobs.map((job) => {
                        const cancellable = ['PENDING', 'RUNNING'].includes(job.status);
                        const replayable = ['FAILED', 'DEAD_LETTER', 'CANCELLED'].includes(
                            job.status
                        );
                        return (
                            <tr key={`${job.kind}:${job.id}`}>
                                <td className='text-xs font-semibold'>{job.kind}</td>
                                <td>
                                    <div className='font-mono text-xs'>
                                        {shortID(job.resource_id)}
                                    </div>
                                    <div className='mt-1 font-mono text-[11px] text-muted'>
                                        {shortID(job.id)}
                                    </div>
                                </td>
                                <td>
                                    <StatusBadge status={job.status} />
                                    {job.error && (
                                        <div
                                            className='mt-1 max-w-72 truncate text-xs text-danger'
                                            title={job.error}
                                        >
                                            {job.error}
                                        </div>
                                    )}
                                </td>
                                <td className='font-mono text-xs'>
                                    {job.attempts}/{job.max_attempts}
                                </td>
                                <td>
                                    <div
                                        className='max-w-52 truncate font-mono text-xs'
                                        title={job.lease_owner}
                                    >
                                        {job.lease_owner ? shortID(job.lease_owner) : '-'}
                                    </div>
                                    <div className='mt-1 text-[11px] text-muted'>
                                        {job.lease_until
                                            ? `Until ${formatTime(job.lease_until)}`
                                            : 'Not leased'}
                                    </div>
                                </td>
                                <td className='whitespace-nowrap text-xs text-muted'>
                                    {formatTime(job.updated_at)}
                                </td>
                                <td>
                                    <div className='flex justify-end gap-1'>
                                        <Tooltip>
                                            <Tooltip.Trigger>
                                                <Button
                                                    isIconOnly
                                                    aria-label='View execution history'
                                                    size='sm'
                                                    variant='ghost'
                                                    onPress={() => loadHistory(job)}
                                                >
                                                    <History className='h-4 w-4' />
                                                </Button>
                                            </Tooltip.Trigger>
                                            <Tooltip.Content>Execution history</Tooltip.Content>
                                        </Tooltip>
                                        {mayMutate(job) && cancellable && (
                                            <Tooltip>
                                                <Tooltip.Trigger>
                                                    <Button
                                                        isIconOnly
                                                        aria-label='Cancel job'
                                                        isDisabled={Boolean(mutating)}
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() => mutate(job, 'cancel')}
                                                    >
                                                        <XCircle className='h-4 w-4' />
                                                    </Button>
                                                </Tooltip.Trigger>
                                                <Tooltip.Content>Cancel</Tooltip.Content>
                                            </Tooltip>
                                        )}
                                        {mayMutate(job) && replayable && (
                                            <Tooltip>
                                                <Tooltip.Trigger>
                                                    <Button
                                                        isIconOnly
                                                        aria-label='Replay job'
                                                        isDisabled={Boolean(mutating)}
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() => mutate(job, 'replay')}
                                                    >
                                                        <RotateCcw className='h-4 w-4' />
                                                    </Button>
                                                </Tooltip.Trigger>
                                                <Tooltip.Content>Replay</Tooltip.Content>
                                            </Tooltip>
                                        )}
                                    </div>
                                </td>
                            </tr>
                        );
                    })}
                </tbody>
            </DataTable>

            {selected && (
                <ContentCard
                    action={<span className='font-mono text-xs text-muted'>{selected.id}</span>}
                    title='Execution history'
                >
                    {historyLoading ? (
                        <div className='h-28 animate-pulse rounded-lg bg-surface-secondary' />
                    ) : executions.length === 0 ? (
                        <div className='py-8 text-center text-sm text-muted'>
                            No execution attempts recorded.
                        </div>
                    ) : (
                        <div className='space-y-3'>
                            {executions.map((execution) => (
                                <div
                                    className='grid gap-3 rounded-lg border border-border p-3 sm:grid-cols-[5rem_8rem_1fr_auto]'
                                    key={execution.id}
                                >
                                    <div className='font-mono text-xs'>
                                        Attempt {execution.attempt}
                                    </div>
                                    <StatusBadge status={execution.status} />
                                    <div className='min-w-0'>
                                        <div
                                            className='truncate font-mono text-xs'
                                            title={execution.worker_id}
                                        >
                                            {execution.worker_id}
                                        </div>
                                        {execution.error && (
                                            <div className='mt-1 text-xs text-danger'>
                                                {execution.error}
                                            </div>
                                        )}
                                    </div>
                                    <div className='text-xs text-muted'>
                                        {formatTime(execution.finished_at ?? execution.started_at)}
                                    </div>
                                </div>
                            ))}
                        </div>
                    )}
                    {(selected.result_json != null || selected.compensation_json != null) && (
                        <div className='mt-4 grid gap-3 lg:grid-cols-2'>
                            {selected.result_json != null && (
                                <pre className='max-h-64 overflow-auto rounded-lg bg-surface-secondary p-3 text-xs'>
                                    {JSON.stringify(selected.result_json, null, 2)}
                                </pre>
                            )}
                            {selected.compensation_json != null && (
                                <pre className='max-h-64 overflow-auto rounded-lg bg-surface-secondary p-3 text-xs'>
                                    {JSON.stringify(selected.compensation_json, null, 2)}
                                </pre>
                            )}
                        </div>
                    )}
                </ContentCard>
            )}
        </div>
    );
}
