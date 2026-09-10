import type { JobExecution, ManagedJob, ManagedJobKind } from '@/api';

import { Button, Input, Tooltip } from '@heroui/react';
import { Eye, FileJson, RefreshCw, RotateCcw, XCircle } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, jobsApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { TablePagination } from '@/components/TablePagination.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { useListQuery } from '@/hooks/useListQuery.ts';
import { JobDetails, kindLabel, kinds, statuses } from '@/pages/jobs/JobDetails.tsx';
import { enumField, integerField, stringField } from '@/utils/listQuery.ts';
import { canManageCluster, canOperateCluster } from '@/utils/rbac.ts';

const jobsQuery = {
    q: stringField(),
    kind: enumField(
        kinds.map((item) => item.value),
        ''
    ),
    status: enumField(
        statuses.map(([value]) => value),
        ''
    ),
    page: integerField(1, { min: 1 }),
    page_size: integerField(25, { allowed: [25, 50, 100] }),
};

function humanize(value: string) {
    return value
        .toLowerCase()
        .replace(/_/g, ' ')
        .replace(/^./, (letter) => letter.toUpperCase());
}

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

export default function Jobs() {
    const navigate = useNavigate();
    const { clusterId, clusters } = useCluster();
    const role = clusters.find((cluster) => cluster.id === clusterId)?.role;
    const canOperate = canOperateCluster(role);
    const canManage = canManageCluster(role);
    const api = useMemo(() => jobsApi(clusterId), [clusterId]);
    const { values, replace, searchInput, setSearchInput } = useListQuery(jobsQuery, {
        searchKey: 'q',
    });
    const { q: search, kind, status, page, page_size: pageSize } = values;
    const [total, setTotal] = useState(0);
    const [jobs, setJobs] = useState<ManagedJob[]>([]);
    const [selected, setSelected] = useState<ManagedJob | null>(null);
    const [selectedDetail, setSelectedDetail] = useState<ManagedJob | null>(null);
    const [executions, setExecutions] = useState<JobExecution[]>([]);
    const [loading, setLoading] = useState(true);
    const [detailLoading, setDetailLoading] = useState(false);
    const [historyLoading, setHistoryLoading] = useState(false);
    const [mutating, setMutating] = useState('');
    const [loadError, setLoadError] = useState('');
    const [detailError, setDetailError] = useState('');
    const [historyError, setHistoryError] = useState('');
    const [actionError, setActionError] = useState('');
    const listRequestVersion = useRef(0);
    const detailRequestVersion = useRef(0);
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
            const result = await api.list({
                kind,
                status,
                query: search || undefined,
                page,
                page_size: pageSize,
            });
            if (
                version !== listRequestVersion.current ||
                requestedClusterID !== activeClusterID.current
            )
                return;
            const lastPage = Math.max(1, Math.ceil(result.total / result.page_size));
            if (page > lastPage) {
                replace({ page: lastPage });
                return;
            }
            setJobs(result.items ?? []);
            setTotal(result.total);
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
    }, [api, clusterId, kind, page, pageSize, replace, search, status]);

    useEffect(() => {
        if (previousClusterID.current === clusterId) return;
        previousClusterID.current = clusterId;
        listRequestVersion.current++;
        detailRequestVersion.current++;
        mutationRequestVersion.current++;
        replace({ page: 1 });
        setJobs([]);
        setTotal(0);
        setSelected(null);
        setSelectedDetail(null);
        setExecutions([]);
        setLoading(Boolean(clusterId));
        setDetailLoading(false);
        setHistoryLoading(false);
        setLoadError('');
        setDetailError('');
        setHistoryError('');
        setActionError('');
    }, [clusterId, replace]);

    useAutoRefresh(load, Boolean(clusterId));

    const loadDetails = useCallback(
        async (job: ManagedJob, refresh = false) => {
            const version = ++detailRequestVersion.current;
            const requestedClusterID = clusterId;
            if (!refresh) {
                setSelected(job);
                setSelectedDetail(null);
                setExecutions([]);
                setDetailError('');
                setHistoryError('');
                setDetailLoading(true);
                setHistoryLoading(true);
            }
            const current = () =>
                version === detailRequestVersion.current &&
                requestedClusterID === activeClusterID.current;
            const detailRequest = api
                .detail(job.kind, job.id)
                .then((detail) => {
                    if (!current()) return;
                    setSelectedDetail(detail);
                    setDetailError('');
                })
                .catch((loadError) => {
                    if (!current()) return;
                    setDetailError(
                        loadError instanceof ApiError
                            ? loadError.message
                            : 'Failed to load job details'
                    );
                })
                .finally(() => {
                    if (current()) setDetailLoading(false);
                });
            const historyRequest = api
                .executions(job.kind, job.id)
                .then((history) => {
                    if (!current()) return;
                    setExecutions(history ?? []);
                    setHistoryError('');
                })
                .catch((loadError) => {
                    if (!current()) return;
                    setHistoryError(
                        loadError instanceof ApiError
                            ? loadError.message
                            : 'Failed to load execution attempts'
                    );
                })
                .finally(() => {
                    if (current()) setHistoryLoading(false);
                });
            await Promise.allSettled([detailRequest, historyRequest]);
        },
        [api, clusterId]
    );

    useEffect(() => {
        if (!selectedDetail || !['PENDING', 'RUNNING'].includes(selectedDetail.status)) return;
        const timer = window.setInterval(() => void loadDetails(selectedDetail, true), 5000);
        return () => window.clearInterval(timer);
    }, [loadDetails, selectedDetail]);

    const mutate = async (job: ManagedJob, action: 'cancel' | 'replay') => {
        const version = ++mutationRequestVersion.current;
        const requestedClusterID = clusterId;
        setMutating(`${action}:${job.id}`);
        setActionError('');
        try {
            const result = await api[action](job.kind, job.id);
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
            if (selected?.id === job.id) {
                const nextJob =
                    action === 'replay'
                        ? {
                              ...job,
                              id: result.id,
                              status: result.mode === 'current' ? 'SUCCEEDED' : 'PENDING',
                          }
                        : job;
                await loadDetails(nextJob);
            }
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
                        <RefreshCw
                            aria-hidden='true'
                            className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`}
                        />
                    </Button>
                }
                subtitle='Inspect job inputs, execution attempts, results, and dead letters.'
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
                    <div className='grid w-full gap-2 sm:grid-cols-2 lg:grid-cols-[minmax(220px,1fr)_150px_150px_110px]'>
                        <Input
                            aria-label='Search jobs'
                            placeholder='Search job, resource, or operation'
                            spellCheck={false}
                            value={searchInput}
                            variant='secondary'
                            onChange={(event) => setSearchInput(event.target.value)}
                        />
                        <SelectField
                            ariaLabel='Job type'
                            options={kinds.map((item) => ({
                                id: item.value,
                                label: item.label,
                            }))}
                            value={kind}
                            variant='secondary'
                            onChange={(value) =>
                                replace({ kind: value as '' | ManagedJobKind }, { resetPage: true })
                            }
                        />
                        <SelectField
                            ariaLabel='Job status'
                            options={statuses.map(([value, label]) => ({ id: value, label }))}
                            value={status}
                            variant='secondary'
                            onChange={(value) => replace({ status: value }, { resetPage: true })}
                        />
                        <SelectField
                            ariaLabel='Rows per page'
                            options={[25, 50, 100].map((value) => ({
                                id: String(value),
                                label: `${value} rows`,
                            }))}
                            value={String(pageSize)}
                            variant='secondary'
                            onChange={(value) =>
                                replace({ page_size: Number(value) }, { resetPage: true })
                            }
                        />
                    </div>
                }
                caption='Background jobs'
                className='[&_th]:tracking-normal'
                compact
                empty={jobs.length === 0}
                emptyDescription='Jobs matching the selected filters will appear here.'
                emptyTitle='No jobs found'
                footer={
                    <TablePagination
                        page={page}
                        pageSize={pageSize}
                        total={total}
                        onPageChange={(nextPage) => replace({ page: nextPage })}
                    />
                }
                loading={loading && jobs.length === 0}
                title={`${total.toLocaleString()} jobs`}
            >
                <thead>
                    <tr>
                        <th>Type</th>
                        <th>Resource</th>
                        <th>Operation</th>
                        <th>Status</th>
                        <th>Attempt</th>
                        <th>Updated</th>
                        <th aria-label='Actions' />
                    </tr>
                </thead>
                <tbody>
                    {jobs.map((job) => {
                        const cancellable =
                            job.kind !== 'AGENT_UPGRADE' &&
                            ['PENDING', 'RUNNING'].includes(job.status);
                        const replayable =
                            job.kind !== 'AGENT_UPGRADE' &&
                            ['FAILED', 'DEAD_LETTER', 'CANCELLED'].includes(job.status);
                        return (
                            <tr key={`${job.kind}:${job.id}`}>
                                <td className='text-xs font-semibold'>{kindLabel(job.kind)}</td>
                                <td className='max-w-72'>
                                    <button
                                        className='block max-w-full text-left hover:text-accent'
                                        type='button'
                                        onClick={() => void loadDetails(job)}
                                    >
                                        <span className='block truncate text-sm font-medium'>
                                            {job.resource_name}
                                        </span>
                                        <span className='mt-0.5 block truncate text-xs text-muted'>
                                            {humanize(job.resource_type)}
                                            {job.resource_hint ? ` | ${job.resource_hint}` : ''}
                                        </span>
                                    </button>
                                </td>
                                <td className='max-w-72'>
                                    <div className='truncate text-xs' title={job.operation}>
                                        {humanize(job.operation)}
                                    </div>
                                </td>
                                <td>
                                    <StatusBadge status={job.status} />
                                    {job.error && (
                                        <div
                                            className='mt-1 max-w-64 truncate text-xs text-danger'
                                            title={job.error}
                                        >
                                            {job.error}
                                        </div>
                                    )}
                                </td>
                                <td className='font-mono text-xs'>
                                    {job.attempts}/{job.max_attempts}
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
                                                    aria-label='View job details'
                                                    size='sm'
                                                    variant='ghost'
                                                    onPress={() => void loadDetails(job)}
                                                >
                                                    <Eye aria-hidden='true' className='h-4 w-4' />
                                                </Button>
                                            </Tooltip.Trigger>
                                            <Tooltip.Content>View details</Tooltip.Content>
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
                                                        onPress={() => void mutate(job, 'cancel')}
                                                    >
                                                        <XCircle
                                                            aria-hidden='true'
                                                            className='h-4 w-4'
                                                        />
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
                                                        onPress={() => void mutate(job, 'replay')}
                                                    >
                                                        <RotateCcw
                                                            aria-hidden='true'
                                                            className='h-4 w-4'
                                                        />
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

            <DialogShell
                icon={<FileJson className='h-5 w-5' />}
                isOpen={selected !== null}
                size='xl'
                subtitle={selected?.resource_name}
                title={selected ? `${kindLabel(selected.kind)} job details` : 'Job details'}
                onOpenChange={(open) => {
                    if (!open) {
                        detailRequestVersion.current++;
                        setSelected(null);
                        setSelectedDetail(null);
                        setExecutions([]);
                        setDetailError('');
                        setHistoryError('');
                    }
                }}
            >
                {detailError && !selectedDetail ? (
                    <div className='m-6 rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                        {detailError}
                    </div>
                ) : detailLoading || !selectedDetail ? (
                    <div className='space-y-4 p-6' role='status'>
                        <div className='h-28 animate-pulse rounded-lg bg-surface-secondary' />
                        <div className='h-48 animate-pulse rounded-lg bg-surface-secondary' />
                    </div>
                ) : (
                    <>
                        {detailError && (
                            <div className='mx-6 mt-5 rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                                {detailError}
                            </div>
                        )}
                        <JobDetails
                            executions={executions}
                            historyError={historyError}
                            historyLoading={historyLoading}
                            job={selectedDetail}
                            onNavigate={() => {
                                const path =
                                    selectedDetail.resource_type === 'SITE'
                                        ? `/sites/${selectedDetail.resource_id}`
                                        : `/nodes/${selectedDetail.resource_id}`;
                                detailRequestVersion.current++;
                                setSelected(null);
                                navigate(path);
                            }}
                        />
                    </>
                )}
                <DialogFooter>
                    {selectedDetail &&
                        mayMutate(selectedDetail) &&
                        selectedDetail.kind !== 'AGENT_UPGRADE' &&
                        ['PENDING', 'RUNNING'].includes(selectedDetail.status) && (
                            <Button
                                isDisabled={Boolean(mutating)}
                                variant='secondary'
                                onPress={() => void mutate(selectedDetail, 'cancel')}
                            >
                                <XCircle aria-hidden='true' className='h-4 w-4' /> Cancel
                            </Button>
                        )}
                    {selectedDetail &&
                        mayMutate(selectedDetail) &&
                        selectedDetail.kind !== 'AGENT_UPGRADE' &&
                        ['FAILED', 'DEAD_LETTER', 'CANCELLED'].includes(selectedDetail.status) && (
                            <Button
                                isDisabled={Boolean(mutating)}
                                variant='secondary'
                                onPress={() => void mutate(selectedDetail, 'replay')}
                            >
                                <RotateCcw aria-hidden='true' className='h-4 w-4' /> Replay
                            </Button>
                        )}
                    <Button
                        variant='primary'
                        onPress={() => {
                            detailRequestVersion.current++;
                            setSelected(null);
                        }}
                    >
                        Close
                    </Button>
                </DialogFooter>
            </DialogShell>
        </div>
    );
}
