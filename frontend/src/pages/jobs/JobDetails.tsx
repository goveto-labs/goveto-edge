import type { ReactNode } from 'react';
import type { JobExecution, ManagedJob, ManagedJobKind } from '@/api';

import { Button, Tabs, Tooltip } from '@heroui/react';
import { Copy, ExternalLink } from 'lucide-react';

import { JsonDiff } from '@/components/JsonDiff.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';

export const kinds: Array<{ value: '' | ManagedJobKind; label: string }> = [
    { value: '', label: 'All types' },
    { value: 'PUBLISH', label: 'Publish' },
    { value: 'PURGE', label: 'Cache refresh' },
    { value: 'INSTALL', label: 'Installation' },
    { value: 'AGENT_UPGRADE', label: 'Agent upgrade' },
    { value: 'DNS', label: 'DNS' },
    { value: 'CERTIFICATE', label: 'Certificate' },
];

export const statuses = [
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

export function kindLabel(kind: ManagedJobKind) {
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

export function JobDetails({
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
