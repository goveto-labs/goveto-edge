import type { ReactNode } from 'react';
import type { JobExecution, ManagedJob, ManagedJobKind } from '@/api';

import { Button, Input, Pagination, Tabs, Tooltip } from '@heroui/react';
import { Copy, ExternalLink, Eye, FileJson, RefreshCw, RotateCcw, XCircle } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';

import { ApiError, jobsApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { JsonDiff } from '@/components/JsonDiff.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
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

type PageItem = number | 'start-ellipsis' | 'end-ellipsis';

function visiblePages(current: number, total: number): PageItem[] {
    if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1);
    if (current <= 4) return [1, 2, 3, 4, 5, 'end-ellipsis', total];
    if (current >= total - 3) {
        return [1, 'start-ellipsis', total - 4, total - 3, total - 2, total - 1, total];
    }
    return [1, 'start-ellipsis', current - 1, current, current + 1, 'end-ellipsis', total];
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

function formatDuration(start?: string, end?: string) {
    if (!start || !end) return '-';
    const milliseconds = new Date(end).getTime() - new Date(start).getTime();
    if (!Number.isFinite(milliseconds) || milliseconds < 0) return '-';
    if (milliseconds < 1000) return `${milliseconds} ms`;
    if (milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(1)} s`;
    const minutes = Math.floor(milliseconds / 60_000);
    const seconds = Math.floor((milliseconds % 60_000) / 1000);
    return `${minutes}m ${seconds}s`;
}

function humanize(value: string) {
    return value
        .toLowerCase()
        .replace(/_/g, ' ')
        .replace(/^./, (letter) => letter.toUpperCase());
}

function kindLabel(kind: ManagedJobKind) {
    return kinds.find((item) => item.value === kind)?.label ?? humanize(kind);
}

function jsonText(value: unknown) {
    if (value === undefined) return '';
    return JSON.stringify(value, null, 2) ?? String(value);
}

function asRecord(value: unknown): Record<string, unknown> | null {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    return value as Record<string, unknown>;
}

function DetailSection({ children, title }: { children: ReactNode; title: string }) {
    return (
        <section>
            <h3 className='border-b border-border pb-2 text-sm font-semibold'>{title}</h3>
            <div className='pt-3'>{children}</div>
        </section>
    );
}

function DetailValue({ label, value, mono }: { label: string; value?: ReactNode; mono?: boolean }) {
    return (
        <div className='min-w-0'>
            <dt className='text-xs text-muted'>{label}</dt>
            <dd className={`mt-1 break-words text-sm ${mono ? 'font-mono text-xs' : ''}`}>
                {value === '' || value === undefined || value === null ? '-' : value}
            </dd>
        </div>
    );
}

function JSONBlock({ title, value }: { title: string; value: unknown }) {
    return (
        <div className='min-w-0'>
            <div className='mb-2 text-xs font-medium text-muted'>{title}</div>
            <pre className='max-h-80 overflow-auto rounded-lg bg-surface-secondary p-3 font-mono text-xs leading-5'>
                {jsonText(value)}
            </pre>
        </div>
    );
}

function CopyButton({ label, value }: { label: string; value: string }) {
    return (
        <Tooltip>
            <Tooltip.Trigger>
                <Button
                    isIconOnly
                    aria-label={`Copy ${label}`}
                    size='sm'
                    variant='ghost'
                    onPress={() => void navigator.clipboard.writeText(value)}
                >
                    <Copy className='h-3.5 w-3.5' />
                </Button>
            </Tooltip.Trigger>
            <Tooltip.Content>Copy {label}</Tooltip.Content>
        </Tooltip>
    );
}

function RawJSON({ title, value }: { title: string; value: unknown }) {
    const text = jsonText(value);
    return (
        <details className='rounded-lg border border-border'>
            <summary className='flex cursor-pointer items-center justify-between px-3 py-2 text-xs font-medium'>
                {title}
                <CopyButton label={title.toLowerCase()} value={text} />
            </summary>
            <pre className='max-h-80 overflow-auto border-t border-border bg-surface-secondary p-3 font-mono text-xs leading-5'>
                {text}
            </pre>
        </details>
    );
}

function SummaryTab({ job, onNavigate }: { job: ManagedJob; onNavigate: () => void }) {
    const hasScheduling =
        ['PENDING', 'RUNNING'].includes(job.status) ||
        Boolean(job.lease_owner || job.lease_until || job.heartbeat_at || job.cancel_requested_at);
    return (
        <div className='space-y-6 pt-3'>
            {job.error && (
                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                    {job.error}
                </div>
            )}
            <DetailSection title='Overview'>
                <dl className='grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-3'>
                    <DetailValue label='Status' value={<StatusBadge status={job.status} />} />
                    <DetailValue label='Resource' value={job.resource_name} />
                    <DetailValue label='Operation' value={humanize(job.operation)} />
                    <DetailValue
                        label='Duration'
                        value={formatDuration(job.created_at, job.updated_at)}
                    />
                    <DetailValue
                        label='Attempts'
                        mono
                        value={`${job.attempts}/${job.max_attempts}`}
                    />
                    <DetailValue label='Resource hint' value={job.resource_hint} />
                    <DetailValue label='Created' value={formatTime(job.created_at)} />
                    <DetailValue label='Updated' value={formatTime(job.updated_at)} />
                    <DetailValue label='Type' value={humanize(job.resource_type)} />
                </dl>
                <div className='mt-5 grid gap-2'>
                    <div className='flex min-w-0 items-center gap-2 rounded-lg bg-surface-secondary px-3 py-2'>
                        <span className='w-24 shrink-0 text-xs text-muted'>Job ID</span>
                        <code className='min-w-0 flex-1 truncate text-xs'>{job.id}</code>
                        <CopyButton label='job ID' value={job.id} />
                    </div>
                    <div className='flex min-w-0 items-center gap-2 rounded-lg bg-surface-secondary px-3 py-2'>
                        <span className='w-24 shrink-0 text-xs text-muted'>Resource ID</span>
                        <code className='min-w-0 flex-1 truncate text-xs'>{job.resource_id}</code>
                        <CopyButton label='resource ID' value={job.resource_id} />
                        {['SITE', 'NODE'].includes(job.resource_type) && (
                            <Button
                                isIconOnly
                                aria-label='Open resource'
                                size='sm'
                                variant='ghost'
                                onPress={onNavigate}
                            >
                                <ExternalLink className='h-3.5 w-3.5' />
                            </Button>
                        )}
                    </div>
                </div>
            </DetailSection>
            {hasScheduling && (
                <DetailSection title='Scheduling and lease'>
                    <dl className='grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-3'>
                        <DetailValue label='Next attempt' value={formatTime(job.next_attempt_at)} />
                        <DetailValue label='Timeout' value={formatTime(job.timeout_at)} />
                        <DetailValue label='Lease owner' mono value={job.lease_owner} />
                        <DetailValue label='Lease until' value={formatTime(job.lease_until)} />
                        <DetailValue label='Heartbeat' value={formatTime(job.heartbeat_at)} />
                        <DetailValue
                            label='Cancel requested'
                            value={formatTime(job.cancel_requested_at)}
                        />
                    </dl>
                </DetailSection>
            )}
            {(job.result_json !== undefined || job.compensation_json !== undefined) && (
                <DetailSection title='Outcome'>
                    <div className='grid gap-4'>
                        {job.result_json !== undefined && (
                            <JSONBlock title='Result' value={job.result_json} />
                        )}
                        {job.compensation_json !== undefined && (
                            <JSONBlock title='Compensation' value={job.compensation_json} />
                        )}
                    </div>
                </DetailSection>
            )}
        </div>
    );
}

function ChangeTab({ job }: { job: ManagedJob }) {
    const input = asRecord(job.input_json);
    if (job.kind !== 'PUBLISH') {
        return (
            <div className='pt-5'>
                <RawJSON title='Raw input' value={job.input_json} />
            </div>
        );
    }
    const after = input?.config;
    const context = job.publish_context;
    return (
        <div className='space-y-6 pt-3'>
            <dl className='grid gap-4 sm:grid-cols-2 lg:grid-cols-4'>
                <DetailValue
                    label='Version'
                    mono
                    value={String(context?.version ?? input?.version ?? '')}
                />
                <DetailValue
                    label='Version status'
                    value={context ? <StatusBadge status={context.status} /> : '-'}
                />
                <DetailValue
                    label='Baseline version'
                    mono
                    value={
                        context?.baseline_version
                            ? String(context.baseline_version)
                            : 'Initial publish'
                    }
                />
                <DetailValue
                    label='Targets'
                    mono
                    value={Array.isArray(input?.targets) ? String(input.targets.length) : '-'}
                />
            </dl>
            <DetailSection title='Configuration change'>
                {context?.baseline_config !== undefined ? (
                    <JsonDiff before={context.baseline_config} after={after} />
                ) : (
                    <div className='space-y-3'>
                        <p className='text-sm text-muted'>
                            No earlier published configuration is available for comparison.
                        </p>
                        <JSONBlock
                            title='Published configuration (secrets redacted)'
                            value={after}
                        />
                    </div>
                )}
            </DetailSection>
            <DetailSection title='Targets'>
                <JSONBlock title='Target resources' value={input?.target_resources ?? []} />
            </DetailSection>
            <RawJSON title='Raw publish input' value={job.input_json} />
        </div>
    );
}

function AttemptsTab({
    executions,
    error,
    loading,
}: {
    executions: JobExecution[];
    error: string;
    loading: boolean;
}) {
    if (loading) return <div className='mt-5 h-28 animate-pulse rounded-lg bg-surface-secondary' />;
    return (
        <div className='space-y-3 pt-3'>
            {error && (
                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                    {error}
                </div>
            )}
            {executions.length === 0 && !error ? (
                <div className='py-8 text-center text-sm text-muted'>
                    No execution attempts recorded.
                </div>
            ) : (
                executions.map((execution) => (
                    <details
                        className='rounded-lg border border-border'
                        key={execution.id}
                        open={Boolean(execution.error)}
                    >
                        <summary className='cursor-pointer px-4 py-3'>
                            <div className='grid gap-2 sm:grid-cols-[7rem_8rem_1fr_auto] sm:items-center'>
                                <span className='font-mono text-xs'>
                                    Attempt {execution.attempt}
                                </span>
                                <span>
                                    <StatusBadge status={execution.status} />
                                </span>
                                <span
                                    className='truncate font-mono text-xs'
                                    title={execution.worker_id}
                                >
                                    {execution.worker_id}
                                </span>
                                <span className='text-xs text-muted'>
                                    {formatDuration(execution.started_at, execution.finished_at)}
                                </span>
                            </div>
                        </summary>
                        <div className='space-y-4 border-t border-border px-4 py-4'>
                            <dl className='grid gap-4 sm:grid-cols-2 lg:grid-cols-4'>
                                <DetailValue
                                    label='Started'
                                    value={formatTime(execution.started_at)}
                                />
                                <DetailValue
                                    label='Finished'
                                    value={formatTime(execution.finished_at)}
                                />
                                <DetailValue
                                    label='Heartbeat'
                                    value={formatTime(execution.heartbeat_at)}
                                />
                                <DetailValue label='Execution ID' mono value={execution.id} />
                            </dl>
                            {execution.error && (
                                <div className='rounded-lg bg-danger/10 px-3 py-2 text-sm text-danger'>
                                    {execution.error}
                                </div>
                            )}
                            {execution.result_json !== undefined && (
                                <JSONBlock title='Attempt result' value={execution.result_json} />
                            )}
                        </div>
                    </details>
                ))
            )}
        </div>
    );
}

function JobDetails({
    job,
    executions,
    historyError,
    historyLoading,
    onNavigate,
}: {
    job: ManagedJob;
    executions: JobExecution[];
    historyError: string;
    historyLoading: boolean;
    onNavigate: () => void;
}) {
    return (
        <div className='max-h-[76vh] overflow-y-auto px-6 py-6'>
            <Tabs className='w-full' aria-label='Job details' defaultSelectedKey='summary'>
                <Tabs.ListContainer>
                    <Tabs.List aria-label='Options'>
                        <Tabs.Tab id='summary'>
                            Summary
                            <Tabs.Indicator />
                        </Tabs.Tab>
                        <Tabs.Tab id='change'>
                            {job.kind === 'PUBLISH' ? 'Change' : 'Input'}
                            <Tabs.Indicator />
                        </Tabs.Tab>
                        <Tabs.Tab id='attempts'>
                            Attempts
                            <Tabs.Indicator />
                        </Tabs.Tab>
                    </Tabs.List>
                </Tabs.ListContainer>
                <Tabs.Panel id='summary'>
                    <SummaryTab job={job} onNavigate={onNavigate} />
                </Tabs.Panel>
                <Tabs.Panel id='change'>
                    <ChangeTab job={job} />
                </Tabs.Panel>
                <Tabs.Panel id='attempts'>
                    <AttemptsTab
                        error={historyError}
                        executions={executions}
                        loading={historyLoading}
                    />
                </Tabs.Panel>
            </Tabs>
        </div>
    );
}

export default function Jobs() {
    const navigate = useNavigate();
    const [urlParams, setUrlParams] = useSearchParams();
    const { clusterId, clusters } = useCluster();
    const role = clusters.find((cluster) => cluster.id === clusterId)?.role;
    const canOperate = canOperateCluster(role);
    const canManage = canManageCluster(role);
    const api = useMemo(() => jobsApi(clusterId), [clusterId]);
    const requestedKind = urlParams.get('kind') || '';
    const [kind, setKind] = useState<'' | ManagedJobKind>(() =>
        kinds.some((item) => item.value === requestedKind)
            ? (requestedKind as '' | ManagedJobKind)
            : ''
    );
    const [status, setStatus] = useState('');
    const [query, setQuery] = useState('');
    const [search, setSearch] = useState('');
    const [page, setPage] = useState(1);
    const [pageSize, setPageSize] = useState(25);
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

    useEffect(() => {
        const timer = window.setTimeout(() => {
            setSearch(query.trim());
            setPage(1);
        }, 300);
        return () => window.clearTimeout(timer);
    }, [query]);

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
                setPage(lastPage);
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
    }, [api, clusterId, kind, page, pageSize, search, status]);

    useEffect(() => {
        if (previousClusterID.current === clusterId) return;
        previousClusterID.current = clusterId;
        listRequestVersion.current++;
        detailRequestVersion.current++;
        mutationRequestVersion.current++;
        setPage(1);
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
    }, [clusterId]);

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
    const pageCount = Math.max(1, Math.ceil(total / pageSize));
    const rangeStart = total === 0 ? 0 : (page - 1) * pageSize + 1;
    const rangeEnd = Math.min(page * pageSize, total);

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
                            value={query}
                            variant='secondary'
                            onChange={(event) => setQuery(event.target.value)}
                        />
                        <SelectField
                            ariaLabel='Job type'
                            options={kinds.map((item) => ({
                                id: item.value,
                                label: item.label,
                            }))}
                            value={kind}
                            variant='secondary'
                            onChange={(value) => {
                                setKind(value as '' | ManagedJobKind);
                                setUrlParams((current) => {
                                    if (value) current.set('kind', value);
                                    else current.delete('kind');
                                    return current;
                                });
                                setPage(1);
                            }}
                        />
                        <SelectField
                            ariaLabel='Job status'
                            options={statuses.map(([value, label]) => ({ id: value, label }))}
                            value={status}
                            variant='secondary'
                            onChange={(value) => {
                                setStatus(value);
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
                    </div>
                }
                className='[&_td]:py-3 [&_th]:tracking-normal'
                empty={jobs.length === 0}
                emptyDescription='Jobs matching the selected filters will appear here.'
                emptyTitle='No jobs found'
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
                        const cancellable = ['PENDING', 'RUNNING'].includes(job.status);
                        const replayable = ['FAILED', 'DEAD_LETTER', 'CANCELLED'].includes(
                            job.status
                        );
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
                                                    <Eye className='h-4 w-4' />
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
                                                        onPress={() => void mutate(job, 'replay')}
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

            {total > 0 && (
                <Pagination className='justify-between' size='sm'>
                    <Pagination.Summary>
                        Showing {rangeStart.toLocaleString()}-{rangeEnd.toLocaleString()} of{' '}
                        {total.toLocaleString()}
                    </Pagination.Summary>
                    <Pagination.Content>
                        <Pagination.Item>
                            <Pagination.Previous
                                isDisabled={page <= 1}
                                onPress={() => setPage((current) => Math.max(1, current - 1))}
                            >
                                <Pagination.PreviousIcon />
                                Previous
                            </Pagination.Previous>
                        </Pagination.Item>
                        {visiblePages(page, pageCount).map((item) =>
                            typeof item === 'number' ? (
                                <Pagination.Item
                                    className={item === page ? undefined : 'hidden sm:block'}
                                    key={item}
                                >
                                    <Pagination.Link
                                        isActive={item === page}
                                        onPress={() => setPage(item)}
                                    >
                                        {item}
                                    </Pagination.Link>
                                </Pagination.Item>
                            ) : (
                                <Pagination.Item className='hidden sm:block' key={item}>
                                    <Pagination.Ellipsis />
                                </Pagination.Item>
                            )
                        )}
                        <Pagination.Item>
                            <Pagination.Next
                                isDisabled={page >= pageCount}
                                onPress={() =>
                                    setPage((current) => Math.min(pageCount, current + 1))
                                }
                            >
                                Next
                                <Pagination.NextIcon />
                            </Pagination.Next>
                        </Pagination.Item>
                    </Pagination.Content>
                </Pagination>
            )}

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
                            historyLoading={historyLoading}
                            historyError={historyError}
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
                        ['PENDING', 'RUNNING'].includes(selectedDetail.status) && (
                            <Button
                                isDisabled={Boolean(mutating)}
                                variant='secondary'
                                onPress={() => void mutate(selectedDetail, 'cancel')}
                            >
                                <XCircle className='h-4 w-4' /> Cancel
                            </Button>
                        )}
                    {selectedDetail &&
                        mayMutate(selectedDetail) &&
                        ['FAILED', 'DEAD_LETTER', 'CANCELLED'].includes(selectedDetail.status) && (
                            <Button
                                isDisabled={Boolean(mutating)}
                                variant='secondary'
                                onPress={() => void mutate(selectedDetail, 'replay')}
                            >
                                <RotateCcw className='h-4 w-4' /> Replay
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
