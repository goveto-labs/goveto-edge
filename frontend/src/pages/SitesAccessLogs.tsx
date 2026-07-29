import type { ReactNode } from 'react';
import type { NodeRequestLog, SiteSummary } from '@/api';

import { Button, Input, Pagination, Tooltip } from '@heroui/react';
import { Eye, FileSearch, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, analyticsApi, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogShell } from '@/components/DialogShell.tsx';
import { FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';

const countryNames =
    typeof Intl.DisplayNames === 'function'
        ? new Intl.DisplayNames(undefined, { type: 'region' })
        : null;

function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const unit = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatDuration(microseconds: number) {
    if (microseconds < 1000) return `${microseconds.toLocaleString()} us`;
    if (microseconds < 1_000_000) return `${(microseconds / 1000).toFixed(1)} ms`;
    return `${(microseconds / 1_000_000).toFixed(2)} s`;
}

function formatLocation(entry: NodeRequestLog) {
    const country = entry.country ? (countryNames?.of(entry.country) ?? entry.country) : '';
    return [country, entry.region].filter(Boolean).join(', ');
}

function normalizeIPAddress(value: string) {
    const mapped = value.match(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/i);
    return mapped?.[1] || value;
}

function isPrivateIPAddress(value: string) {
    const normalized = normalizeIPAddress(value).toLowerCase();
    const octets = normalized.split('.').map(Number);
    if (octets.length === 4 && octets.every((part) => Number.isInteger(part))) {
        return (
            octets[0] === 10 ||
            octets[0] === 127 ||
            (octets[0] === 169 && octets[1] === 254) ||
            (octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31) ||
            (octets[0] === 192 && octets[1] === 168)
        );
    }
    return normalized === '::1' || normalized.startsWith('fc') || normalized.startsWith('fd');
}

function geoLabel(entry: NodeRequestLog) {
    return (
        formatLocation(entry) ||
        (isPrivateIPAddress(entry.client_ip) ? 'Private network' : 'GEO unavailable')
    );
}

function requestURL(entry: NodeRequestLog) {
    const query = entry.query_string ? `?${entry.query_string}` : '';
    return `${entry.scheme || 'http'}://${entry.hostname}${entry.path}${query}`;
}

function statusClass(status: number) {
    if (status >= 500) return 'bg-danger/15 text-danger';
    if (status >= 400) return 'bg-warning/15 text-warning-foreground';
    if (status >= 300) return 'bg-accent/15 text-accent';
    return 'bg-success/15 text-success';
}

type PageItem = number | 'start-ellipsis' | 'end-ellipsis';

function visiblePages(current: number, total: number): PageItem[] {
    if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1);
    if (current <= 4) return [1, 2, 3, 4, 5, 'end-ellipsis', total];
    if (current >= total - 3) {
        return [1, 'start-ellipsis', total - 4, total - 3, total - 2, total - 1, total];
    }
    return [1, 'start-ellipsis', current - 1, current, current + 1, 'end-ellipsis', total];
}

function DetailSection({ children, title }: { children: ReactNode; title: string }) {
    return (
        <section>
            <h3 className='border-b border-border pb-2 text-sm font-semibold'>{title}</h3>
            <dl className='grid gap-x-6 gap-y-3 pt-3 sm:grid-cols-2'>{children}</dl>
        </section>
    );
}

function DetailValue({
    label,
    value,
    wide,
    mono,
}: {
    label: string;
    value?: ReactNode;
    wide?: boolean;
    mono?: boolean;
}) {
    return (
        <div className={wide ? 'sm:col-span-2' : undefined}>
            <dt className='text-xs text-muted'>{label}</dt>
            <dd className={`mt-1 break-words text-sm ${mono ? 'font-mono text-xs' : ''}`}>
                {value === '' || value === undefined || value === null ? '-' : value}
            </dd>
        </div>
    );
}

function RequestDetails({ entry }: { entry: NodeRequestLog }) {
    const ingress = entry.request_header_bytes + entry.request_body_bytes;
    const egress = entry.response_header_bytes + entry.response_body_bytes;
    const location = geoLabel(entry);

    return (
        <div className='max-h-[72vh] space-y-6 overflow-y-auto p-6'>
            <DetailSection title='Request'>
                <DetailValue label='Time' value={new Date(entry.event_time).toLocaleString()} />
                <DetailValue label='Method' mono value={entry.method} />
                <DetailValue label='URL' mono value={requestURL(entry)} wide />
                <DetailValue label='Protocol' value={entry.protocol || entry.scheme} />
                <DetailValue label='Request ID' mono value={entry.request_id} />
                <DetailValue label='Node ID' mono value={entry.node_id} wide />
                <DetailValue label='Config version' mono value={entry.config_version} />
                <DetailValue label='Source log ID' mono value={entry.source_log_id} />
            </DetailSection>

            <DetailSection title='Client'>
                <DetailValue label='IP address' mono value={normalizeIPAddress(entry.client_ip)} />
                <DetailValue label='GEO' value={location} />
                <DetailValue label='User agent' value={entry.user_agent} wide />
                <DetailValue label='Referer' mono value={entry.referer} wide />
            </DetailSection>

            <DetailSection title='Response'>
                <DetailValue label='Status' mono value={entry.status_code} />
                <DetailValue label='Duration' value={formatDuration(entry.duration_us)} />
                <DetailValue label='Content type' value={entry.content_type} />
                <DetailValue label='File extension' mono value={entry.file_extension} />
                <DetailValue
                    label='Request headers'
                    value={formatBytes(entry.request_header_bytes)}
                />
                <DetailValue label='Request body' value={formatBytes(entry.request_body_bytes)} />
                <DetailValue
                    label='Response headers'
                    value={formatBytes(entry.response_header_bytes)}
                />
                <DetailValue label='Response body' value={formatBytes(entry.response_body_bytes)} />
                <DetailValue label='Ingress' value={formatBytes(ingress)} />
                <DetailValue label='Egress' value={formatBytes(egress)} />
            </DetailSection>

            <DetailSection title='Delivery'>
                <DetailValue label='Cache status' mono value={entry.cache_status} />
                <DetailValue label='Upstream status' mono value={entry.upstream_status || ''} />
                <DetailValue label='Upstream address' mono value={entry.upstream_address} wide />
                <DetailValue label='Proxy error' mono value={entry.handler_error} wide />
            </DetailSection>

            {(entry.waf_action || entry.waf_rule_id || entry.waf_match) && (
                <DetailSection title='Security'>
                    <DetailValue label='WAF action' value={entry.waf_action} />
                    <DetailValue label='Rule ID' mono value={entry.waf_rule_id} />
                    <DetailValue label='Source' value={entry.waf_source} />
                    <DetailValue label='Match' mono value={entry.waf_match} />
                    <DetailValue label='Tags' value={entry.waf_tags} wide />
                </DetailSection>
            )}
        </div>
    );
}

interface SitesAccessLogsProps {
    embeddedSiteId?: string;
}

export function SiteAccessLogsView({ embeddedSiteId }: SitesAccessLogsProps) {
    const { clusterId } = useCluster();
    const sites = useMemo(() => sitesApi(clusterId), [clusterId]);
    const analytics = useMemo(() => analyticsApi(clusterId), [clusterId]);
    const requestSequence = useRef(0);
    const [siteItems, setSiteItems] = useState<SiteSummary[]>([]);
    const [siteId, setSiteId] = useState('');
    const [logs, setLogs] = useState<NodeRequestLog[]>([]);
    const [query, setQuery] = useState('');
    const [search, setSearch] = useState('');
    const [page, setPage] = useState(1);
    const [pageSize, setPageSize] = useState(25);
    const [total, setTotal] = useState(0);
    const [selected, setSelected] = useState<NodeRequestLog | null>(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');
    const activeSiteId = embeddedSiteId || siteId;

    useEffect(() => {
        const timer = window.setTimeout(() => {
            setSearch(query.trim());
            setPage(1);
        }, 300);
        return () => window.clearTimeout(timer);
    }, [query]);

    const loadSites = useCallback(async () => {
        if (!clusterId || embeddedSiteId) return;
        try {
            const items = await sites.list();
            setSiteItems(items);
            setSiteId((current) =>
                items.some((item) => item.id === current) ? current : (items[0]?.id ?? '')
            );
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load sites');
        }
    }, [clusterId, embeddedSiteId, sites]);

    const loadLogs = useCallback(async () => {
        const sequence = ++requestSequence.current;
        if (!clusterId || !activeSiteId) {
            setLogs([]);
            setTotal(0);
            return;
        }
        setLoading(true);
        try {
            const result = await analytics.siteLogs(activeSiteId, {
                page,
                page_size: pageSize,
                query: search || undefined,
            });
            if (sequence !== requestSequence.current) return;
            const lastPage = Math.max(1, Math.ceil(result.total / result.page_size));
            if (page > lastPage) {
                setPage(lastPage);
                return;
            }
            setLogs(result.items);
            setTotal(result.total);
            setError('');
        } catch (loadError) {
            if (sequence !== requestSequence.current) return;
            setError(
                loadError instanceof ApiError ? loadError.message : 'Failed to load access logs'
            );
        } finally {
            if (sequence === requestSequence.current) setLoading(false);
        }
    }, [activeSiteId, analytics, clusterId, page, pageSize, search]);

    useAutoRefresh(loadSites, Boolean(clusterId && !embeddedSiteId));
    useAutoRefresh(loadLogs, Boolean(clusterId && activeSiteId));

    const pageCount = Math.max(1, Math.ceil(total / pageSize));
    const rangeStart = total === 0 ? 0 : (page - 1) * pageSize + 1;
    const rangeEnd = Math.min(page * pageSize, total);

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='Inspect request activity by site.' title='Site access logs' />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to inspect site logs.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-5'>
            {!embeddedSiteId && (
                <PageHeader
                    subtitle='Inspect requests, client location, delivery behavior, and security decisions.'
                    title='Site access logs'
                />
            )}

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <ContentCard allowOverflow>
                <div
                    className={`grid gap-3 md:grid-cols-2 md:items-end ${
                        embeddedSiteId
                            ? 'lg:grid-cols-[minmax(260px,1fr)_140px_auto]'
                            : 'lg:grid-cols-[minmax(220px,1fr)_minmax(260px,1.4fr)_140px_auto]'
                    }`}
                >
                    {!embeddedSiteId && (
                        <SelectField
                            className='w-full'
                            id='site-log-site'
                            label='Site'
                            options={
                                siteItems.length === 0
                                    ? [{ id: '', label: 'No sites available' }]
                                    : siteItems.map((site) => ({
                                          id: site.id,
                                          label: `${site.name} (${site.domains?.[0] || site.id})`,
                                      }))
                            }
                            placeholder='Select a site'
                            value={siteId}
                            variant='secondary'
                            onChange={(value) => {
                                setSiteId(value);
                                setPage(1);
                            }}
                        />
                    )}
                    <FormField htmlFor='access-log-search' label='Search requests'>
                        <Input
                            id='access-log-search'
                            placeholder='Path, IP, request ID, status, user agent'
                            value={query}
                            variant='secondary'
                            onChange={(event) => setQuery(event.target.value)}
                        />
                    </FormField>
                    <SelectField
                        ariaLabel='Rows per page'
                        label='Rows per page'
                        options={[25, 50, 100].map((value) => ({
                            id: String(value),
                            label: String(value),
                        }))}
                        value={String(pageSize)}
                        variant='secondary'
                        onChange={(value) => {
                            setPageSize(Number(value));
                            setPage(1);
                        }}
                    />
                    <Button
                        isDisabled={loading || !activeSiteId}
                        variant='secondary'
                        onPress={() => void loadLogs()}
                    >
                        <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
                        Refresh
                    </Button>
                </div>
            </ContentCard>

            <DataTable
                aria-label='Site access logs'
                className='[&_td]:py-2.5 [&_th]:tracking-normal'
                empty={logs.length === 0}
                emptyDescription='Requests received by the selected site will appear here.'
                emptyTitle={activeSiteId ? 'No matching access logs' : 'Select a site'}
                loading={loading}
                title={`${total.toLocaleString()} requests`}
            >
                <thead>
                    <tr>
                        <th>Time</th>
                        <th>Request</th>
                        <th>Client</th>
                        <th>Status</th>
                        <th>Delivery</th>
                        <th>Duration</th>
                        <th aria-label='Request details' />
                    </tr>
                </thead>
                <tbody>
                    {logs.map((entry) => {
                        return (
                            <tr
                                className='cursor-pointer'
                                key={`${entry.node_id}-${entry.source_log_id}`}
                                onClick={() => setSelected(entry)}
                            >
                                <td className='whitespace-nowrap text-xs text-muted'>
                                    <div>{new Date(entry.event_time).toLocaleDateString()}</div>
                                    <div className='font-mono'>
                                        {new Date(entry.event_time).toLocaleTimeString()}
                                    </div>
                                </td>
                                <td className='max-w-[34rem]'>
                                    <div className='flex min-w-0 items-center gap-2'>
                                        <span className='shrink-0 font-mono text-xs font-semibold'>
                                            {entry.method}
                                        </span>
                                        <span className='truncate text-sm font-medium'>
                                            {entry.hostname}
                                        </span>
                                    </div>
                                    <div className='truncate font-mono text-xs text-muted'>
                                        {entry.path}
                                        {entry.query_string ? `?${entry.query_string}` : ''}
                                    </div>
                                </td>
                                <td>
                                    <div className='whitespace-nowrap font-mono text-xs'>
                                        {normalizeIPAddress(entry.client_ip) || '-'}
                                    </div>
                                    <div className='max-w-52 truncate text-xs text-muted'>
                                        {geoLabel(entry)}
                                    </div>
                                </td>
                                <td>
                                    <span
                                        className={`inline-flex min-h-6 items-center rounded-full px-2 py-0.5 font-mono text-xs font-semibold ${statusClass(entry.status_code)}`}
                                    >
                                        {entry.status_code}
                                    </span>
                                </td>
                                <td>
                                    <div className='text-xs font-medium'>
                                        {entry.cache_status || 'No cache status'}
                                    </div>
                                    <div className='max-w-44 truncate font-mono text-xs text-muted'>
                                        {entry.upstream_address || 'No upstream'}
                                    </div>
                                </td>
                                <td className='whitespace-nowrap text-sm'>
                                    {formatDuration(entry.duration_us)}
                                </td>
                                <td>
                                    <Tooltip>
                                        <Tooltip.Trigger>
                                            <Button
                                                isIconOnly
                                                aria-label='View request details'
                                                size='sm'
                                                variant='ghost'
                                                onPress={() => setSelected(entry)}
                                            >
                                                <Eye className='h-4 w-4' />
                                            </Button>
                                        </Tooltip.Trigger>
                                        <Tooltip.Content>View request details</Tooltip.Content>
                                    </Tooltip>
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
                icon={<FileSearch className='h-5 w-5' />}
                isOpen={selected !== null}
                size='lg'
                subtitle={selected ? new Date(selected.event_time).toLocaleString() : undefined}
                title='Request details'
                onOpenChange={(open) => {
                    if (!open) setSelected(null);
                }}
            >
                {selected && <RequestDetails entry={selected} />}
            </DialogShell>
        </div>
    );
}

export default function SitesAccessLogs() {
    return <SiteAccessLogsView />;
}
