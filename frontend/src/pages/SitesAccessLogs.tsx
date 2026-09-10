import type { ReactNode } from 'react';
import type { NodeRequestLog, SiteSummary } from '@/api';

import { Button, Input, Tooltip } from '@heroui/react';
import { Eye, FileSearch, RefreshCw } from 'lucide-react';
import { useCallback, useMemo, useRef, useState } from 'react';

import { ApiError, analyticsApi, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogShell } from '@/components/DialogShell.tsx';
import { FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { TablePagination } from '@/components/TablePagination.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { useListQuery } from '@/hooks/useListQuery.ts';
import { integerField, stringField } from '@/utils/listQuery.ts';

const countryNames =
    typeof Intl.DisplayNames === 'function'
        ? new Intl.DisplayNames(undefined, { type: 'region' })
        : null;

function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const unit = Math.max(
        0,
        Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
    );
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

const accessLogsQuery = {
    site: stringField(),
    q: stringField(),
    page: integerField(1, { min: 1 }),
    page_size: integerField(25, { allowed: [25, 50, 100] }),
};

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
            <dd className={`mt-1 break-words text-sm tabular ${mono ? 'font-mono text-xs' : ''}`}>
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
                <DetailValue label='Site ID' mono value={entry.site_id} />
                <DetailValue label='Node ID' mono value={entry.node_id} />
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
    const [logs, setLogs] = useState<NodeRequestLog[]>([]);
    const [total, setTotal] = useState(0);
    const [selected, setSelected] = useState<NodeRequestLog | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const { values, replace, searchInput, setSearchInput } = useListQuery(accessLogsQuery, {
        searchKey: 'q',
    });
    const siteId = values.site;
    const search = values.q;
    const page = values.page;
    const pageSize = values.page_size;
    const activeSiteId = embeddedSiteId || siteId;

    const loadSites = useCallback(async () => {
        if (!clusterId || embeddedSiteId) return;
        try {
            const items = await sites.list();
            setSiteItems(items);
            if (siteId && !items.some((item) => item.id === siteId)) {
                replace({ site: '' }, { resetPage: true });
            }
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load sites');
        }
    }, [clusterId, embeddedSiteId, replace, siteId, sites]);

    const loadLogs = useCallback(async () => {
        const sequence = ++requestSequence.current;
        if (!clusterId || (embeddedSiteId && !activeSiteId)) {
            setLogs([]);
            setTotal(0);
            return;
        }
        setLoading(true);
        try {
            const result = await analytics.siteLogs({
                site_id: activeSiteId || undefined,
                page,
                page_size: pageSize,
                query: search || undefined,
            });
            if (sequence !== requestSequence.current) return;
            const lastPage = Math.max(1, Math.ceil(result.total / result.page_size) || 1);
            if (page > lastPage) {
                replace({ page: lastPage });
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
    }, [activeSiteId, analytics, clusterId, embeddedSiteId, page, pageSize, replace, search]);

    useAutoRefresh(loadSites, Boolean(clusterId && !embeddedSiteId));
    useAutoRefresh(loadLogs, Boolean(clusterId && (!embeddedSiteId || activeSiteId)));

    const siteLabel = useCallback(
        (id?: string) => {
            if (!id) return '-';
            const match = siteItems.find((item) => item.id === id);
            return match ? match.name : id;
        },
        [siteItems]
    );

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
                            options={[
                                { id: '', label: 'All sites' },
                                ...siteItems.map((site) => ({
                                    id: site.id,
                                    label: `${site.name} (${site.domains?.[0] || site.id})`,
                                })),
                            ]}
                            placeholder='All sites'
                            value={siteId}
                            variant='secondary'
                            onChange={(value) => replace({ site: value }, { resetPage: true })}
                        />
                    )}
                    <FormField htmlFor='access-log-search' label='Search requests'>
                        <Input
                            id='access-log-search'
                            placeholder='Path, IP, request ID, status, user agent'
                            spellCheck={false}
                            value={searchInput}
                            variant='secondary'
                            onChange={(event) => setSearchInput(event.target.value)}
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
                        onChange={(value) =>
                            replace({ page_size: Number(value) }, { resetPage: true })
                        }
                    />
                    <Button
                        isDisabled={loading || Boolean(embeddedSiteId && !activeSiteId)}
                        variant='secondary'
                        onPress={() => void loadLogs()}
                    >
                        <RefreshCw
                            aria-hidden='true'
                            className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`}
                        />
                        Refresh
                    </Button>
                </div>
            </ContentCard>

            <DataTable
                aria-label='Site access logs'
                className='[&_td]:py-2.5 [&_th]:tracking-normal'
                empty={logs.length === 0}
                emptyDescription={
                    activeSiteId
                        ? 'Requests received by the selected site will appear here.'
                        : 'Requests received across all sites will appear here.'
                }
                emptyTitle='No matching access logs'
                footer={
                    <TablePagination
                        page={page}
                        pageSize={pageSize}
                        total={total}
                        onPageChange={(nextPage) => replace({ page: nextPage })}
                    />
                }
                loading={loading && Boolean(clusterId && (!embeddedSiteId || activeSiteId))}
                title={`${total.toLocaleString()} requests`}
            >
                <thead>
                    <tr>
                        <th>Time</th>
                        {!embeddedSiteId && !activeSiteId && <th>Site</th>}
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
                                {!embeddedSiteId && !activeSiteId && (
                                    <td className='max-w-40'>
                                        <div className='truncate text-sm font-medium'>
                                            {siteLabel(entry.site_id)}
                                        </div>
                                        <div className='truncate font-mono text-xs text-muted'>
                                            {entry.site_id || '-'}
                                        </div>
                                    </td>
                                )}
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
                                                <Eye aria-hidden='true' className='h-4 w-4' />
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
