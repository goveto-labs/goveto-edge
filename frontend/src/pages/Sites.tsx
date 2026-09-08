import type { SiteBundle, SiteSummary, SiteTemplate } from '@/api';

import { Button, Input } from '@heroui/react';
import { Copy, Download, Eye, FileStack, Globe2, Plus, Power, Send, Upload } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { TablePagination } from '@/components/TablePagination.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { useListQuery } from '@/hooks/useListQuery.ts';
import { enumField, integerField, stringField } from '@/utils/listQuery.ts';
import { matchesQuery, paginateItems } from '@/utils/pagination.ts';
import { canOperateCluster } from '@/utils/rbac.ts';

const sitesQuery = {
    q: stringField(),
    status: enumField(['', 'ACTIVE', 'DISABLED'] as const, ''),
    page: integerField(1, { min: 1 }),
    page_size: integerField(25, { allowed: [25, 50, 100] }),
};

function formatBandwidth(bitsPerSecond: number) {
    if (!Number.isFinite(bitsPerSecond) || bitsPerSecond <= 0) return '0 bps';
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
    const unit = Math.min(Math.floor(Math.log(bitsPerSecond) / Math.log(1000)), units.length - 1);
    return `${(bitsPerSecond / 1000 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

export default function Sites() {
    const navigate = useNavigate();
    const { clusterId, clusters } = useCluster();
    const canOperate = canOperateCluster(
        clusters.find((clusterItem) => clusterItem.id === clusterId)?.role
    );
    const api = useMemo(() => sitesApi(clusterId), [clusterId]);
    const [sites, setSites] = useState<SiteSummary[]>([]);
    const [templates, setTemplates] = useState<SiteTemplate[]>([]);
    const [selected, setSelected] = useState<Set<string>>(new Set());
    const importRef = useRef<HTMLInputElement>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const { values, replace, searchInput, setSearchInput } = useListQuery(sitesQuery, {
        searchKey: 'q',
    });
    const selectionFiltersRef = useRef({ clusterId, q: values.q, status: values.status });

    const loadSites = useCallback(
        async (signal?: AbortSignal) => {
            if (!clusterId) return;
            setLoading(true);
            try {
                const siteItems = await api.list({ signal });
                if (signal?.aborted) return;
                setSites(siteItems);
                setSelected(
                    (current) =>
                        new Set(
                            [...current].filter((id) => siteItems.some((site) => site.id === id))
                        )
                );
                try {
                    const templateItems = await api.listTemplates({ signal });
                    if (signal?.aborted) return;
                    setTemplates(templateItems);
                    setError('');
                } catch (templateError) {
                    if (signal?.aborted) return;
                    setTemplates([]);
                    setError(
                        templateError instanceof ApiError
                            ? `Sites loaded, but templates are unavailable: ${templateError.message}`
                            : 'Sites loaded, but templates are unavailable'
                    );
                }
            } catch (loadError) {
                if (signal?.aborted) return;
                setError(
                    loadError instanceof ApiError ? loadError.message : 'Failed to load sites'
                );
            } finally {
                if (!signal?.aborted) setLoading(false);
            }
        },
        [api, clusterId]
    );

    useAutoRefresh(loadSites, Boolean(clusterId));

    const filteredSites = useMemo(
        () =>
            sites.filter((site) => {
                if (values.status && site.status !== values.status) return false;
                return matchesQuery(values.q, site.name, site.id, site.domains);
            }),
        [sites, values.q, values.status]
    );
    useEffect(() => {
        const previous = selectionFiltersRef.current;
        selectionFiltersRef.current = { clusterId, q: values.q, status: values.status };
        if (
            previous.clusterId === clusterId &&
            previous.q === values.q &&
            previous.status === values.status
        ) {
            return;
        }
        setSelected((current) => (current.size === 0 ? current : new Set()));
    }, [clusterId, values.q, values.status]);
    const pagedSites = paginateItems(filteredSites, values.page, values.page_size);
    useEffect(() => {
        if (pagedSites.page !== values.page) replace({ page: pagedSites.page });
    }, [pagedSites.page, replace, values.page]);

    const runBulk = async (action: 'ENABLE' | 'DISABLE' | 'PUBLISH') => {
        if (selected.size === 0) return;
        setLoading(true);
        try {
            const results = await api.bulk({ site_ids: [...selected], action });
            const failures = results.filter((result) => !result.ok);
            setError(failures.map((result) => `${result.site_id}: ${result.error}`).join('; '));
            await loadSites();
        } catch (bulkError) {
            setError(bulkError instanceof ApiError ? bulkError.message : 'Bulk operation failed');
        } finally {
            setLoading(false);
        }
    };

    const exportSelected = async () => {
        if (selected.size === 0) return;
        try {
            const bundles = await Promise.all([...selected].map((id) => api.export(id)));
            const blob = new Blob([JSON.stringify(bundles, null, 2)], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            const anchor = document.createElement('a');
            anchor.href = url;
            anchor.download = `goveto-sites-${new Date().toISOString().slice(0, 10)}.json`;
            anchor.click();
            URL.revokeObjectURL(url);
        } catch (exportError) {
            setError(exportError instanceof ApiError ? exportError.message : 'Export failed');
        }
    };

    const importFile = async (file: File) => {
        try {
            const parsed = JSON.parse(await file.text()) as SiteBundle | SiteBundle[];
            const bundles = Array.isArray(parsed) ? parsed : [parsed];
            const results = await api.import(bundles);
            const failures = results.filter((result) => !result.ok);
            setError(
                failures
                    .map((result) => result.error)
                    .filter(Boolean)
                    .join('; ')
            );
            await loadSites();
        } catch (importError) {
            setError(
                importError instanceof ApiError ? importError.message : 'Import file is invalid'
            );
        }
    };

    const cloneSite = async (site: SiteSummary) => {
        const name = window.prompt('Name for the copied site', `${site.name} copy`)?.trim();
        if (!name) return;
        const domains = window
            .prompt('Domains, separated by commas')
            ?.split(',')
            .map((domain) => domain.trim())
            .filter(Boolean);
        if (!domains?.length) return;
        try {
            const result = await api.clone(site.id, { name, domains });
            navigate(`/sites/${result.id}/settings/basic`);
        } catch (cloneError) {
            setError(cloneError instanceof ApiError ? cloneError.message : 'Copy failed');
        }
    };

    const saveTemplate = async (site: SiteSummary) => {
        const name = window.prompt('Template name', site.name)?.trim();
        if (!name) return;
        try {
            await api.createTemplate({ name, site_id: site.id });
            await loadSites();
        } catch (templateError) {
            setError(
                templateError instanceof ApiError
                    ? templateError.message
                    : 'Template creation failed'
            );
        }
    };

    const createFromTemplate = async () => {
        if (templates.length === 0) return;
        const list = templates.map((item, index) => `${index + 1}: ${item.name}`).join('\n');
        const choice = Number(window.prompt(`Choose a template:\n${list}`));
        if (!Number.isInteger(choice) || choice < 1 || choice > templates.length) return;
        try {
            const template = await api.getTemplate(templates[choice - 1].id);
            if (!template.config) return;
            const name = window.prompt('Site name', template.config.name)?.trim();
            const domains = window
                .prompt('Domains, separated by commas')
                ?.split(',')
                .map((domain) => domain.trim())
                .filter(Boolean);
            if (!name || !domains?.length) return;
            const results = await api.import([
                { ...template.config, name, domains, status: 'ACTIVE' },
            ]);
            const created = results.find((result) => result.ok);
            if (created) navigate(`/sites/${created.site_id}/settings/basic`);
            else setError(results[0]?.error || 'Template import failed');
        } catch (templateError) {
            setError(
                templateError instanceof ApiError
                    ? templateError.message
                    : 'Template creation failed'
            );
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Manage domains, origins, and delivery policies.'
                    title='Sites'
                />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to manage sites.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader subtitle='Manage domains, origins, and delivery policies.' title='Sites'>
                {canOperate && (
                    <div className='flex flex-wrap gap-2'>
                        <input
                            accept='application/json,.json'
                            className='hidden'
                            ref={importRef}
                            type='file'
                            onChange={(event) => {
                                const file = event.target.files?.[0];
                                if (file) void importFile(file);
                                event.target.value = '';
                            }}
                        />
                        <Button variant='secondary' onPress={() => importRef.current?.click()}>
                            <Upload className='mr-2 h-4 w-4' /> Import
                        </Button>
                        <Button
                            isDisabled={templates.length === 0}
                            variant='secondary'
                            onPress={() => void createFromTemplate()}
                        >
                            <FileStack className='mr-2 h-4 w-4' /> From template
                        </Button>
                        <Button onPress={() => navigate('/sites/create')}>
                            <Plus className='mr-2 h-4 w-4' /> Create site
                        </Button>
                    </div>
                )}
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            {canOperate && selected.size > 0 && (
                <div className='flex flex-wrap items-center gap-2 border-y border-border bg-surface-secondary/40 px-4 py-3'>
                    <span className='mr-auto text-sm font-medium'>{selected.size} selected</span>
                    <Button size='sm' variant='secondary' onPress={() => void runBulk('ENABLE')}>
                        <Power className='mr-1.5 h-3.5 w-3.5' /> Enable
                    </Button>
                    <Button size='sm' variant='secondary' onPress={() => void runBulk('DISABLE')}>
                        <Power className='mr-1.5 h-3.5 w-3.5' /> Disable
                    </Button>
                    <Button size='sm' variant='secondary' onPress={() => void runBulk('PUBLISH')}>
                        <Send className='mr-1.5 h-3.5 w-3.5' /> Publish
                    </Button>
                    <Button size='sm' variant='secondary' onPress={() => void exportSelected()}>
                        <Download className='mr-1.5 h-3.5 w-3.5' /> Export
                    </Button>
                </div>
            )}

            <DataTable
                aria-label='Sites'
                action={
                    <div className='grid w-full gap-2 sm:grid-cols-2 lg:grid-cols-[minmax(220px,1fr)_150px_110px]'>
                        <Input
                            aria-label='Search sites'
                            placeholder='Search name or domain'
                            value={searchInput}
                            variant='secondary'
                            onChange={(event) => setSearchInput(event.target.value)}
                        />
                        <SelectField
                            ariaLabel='Site status'
                            options={[
                                { id: '', label: 'All statuses' },
                                { id: 'ACTIVE', label: 'Active' },
                                { id: 'DISABLED', label: 'Disabled' },
                            ]}
                            value={values.status}
                            variant='secondary'
                            onChange={(value) =>
                                replace(
                                    { status: value as typeof values.status },
                                    { resetPage: true }
                                )
                            }
                        />
                        <SelectField
                            ariaLabel='Rows per page'
                            options={[25, 50, 100].map((size) => ({
                                id: String(size),
                                label: `${size} rows`,
                            }))}
                            value={String(values.page_size)}
                            variant='secondary'
                            onChange={(value) =>
                                replace({ page_size: Number(value) }, { resetPage: true })
                            }
                        />
                    </div>
                }
                caption='Sites'
                empty={pagedSites.items.length === 0}
                emptyAction={
                    canOperate && sites.length === 0 ? (
                        <Button onPress={() => navigate('/sites/create')}>
                            <Plus className='mr-2 h-4 w-4' />
                            Create site
                        </Button>
                    ) : undefined
                }
                emptyDescription={
                    sites.length === 0
                        ? 'Create a site to route domains to origin servers.'
                        : 'No sites match the current filters.'
                }
                emptyTitle={sites.length === 0 ? 'No sites yet' : 'No matching sites'}
                footer={
                    <TablePagination
                        page={pagedSites.page}
                        pageSize={values.page_size}
                        total={pagedSites.total}
                        onPageChange={(nextPage) => replace({ page: nextPage })}
                    />
                }
                loading={loading}
                title={`${pagedSites.total.toLocaleString()} sites`}
            >
                <thead>
                    <tr>
                        {canOperate && (
                            <th className='w-10'>
                                <input
                                    aria-label='Select all sites on this page'
                                    checked={
                                        pagedSites.items.length > 0 &&
                                        pagedSites.items.every((site) => selected.has(site.id))
                                    }
                                    type='checkbox'
                                    onChange={(event) =>
                                        setSelected((current) => {
                                            const next = new Set(current);
                                            for (const site of pagedSites.items) {
                                                if (event.target.checked) next.add(site.id);
                                                else next.delete(site.id);
                                            }
                                            return next;
                                        })
                                    }
                                />
                            </th>
                        )}
                        <th>Name</th>
                        <th>Domains</th>
                        <th>Status</th>
                        <th>Bandwidth</th>
                        <th>QPS</th>
                        <th>Certificates</th>
                        <th>Updated</th>
                        <th className='text-right'>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    {pagedSites.items.map((site) => (
                        <tr key={site.id}>
                            {canOperate && (
                                <td>
                                    <input
                                        aria-label={`Select ${site.name}`}
                                        checked={selected.has(site.id)}
                                        type='checkbox'
                                        onChange={(event) =>
                                            setSelected((current) => {
                                                const next = new Set(current);
                                                if (event.target.checked) next.add(site.id);
                                                else next.delete(site.id);
                                                return next;
                                            })
                                        }
                                    />
                                </td>
                            )}
                            <td>
                                <button
                                    className='flex items-center gap-2 text-sm font-semibold hover:underline'
                                    type='button'
                                    onClick={() => navigate(`/sites/${site.id}/overview`)}
                                >
                                    <Globe2 className='h-4 w-4 text-muted' />
                                    {site.name}
                                </button>
                            </td>
                            <td className='max-w-sm'>
                                <span className='line-clamp-2 text-sm text-muted'>
                                    {(site.domains ?? []).join(', ') || '-'}
                                </span>
                            </td>
                            <td>
                                <StatusBadge status={site.status} />
                            </td>
                            <td className='whitespace-nowrap text-sm'>
                                {formatBandwidth(site.bandwidth_bps)}
                            </td>
                            <td className='whitespace-nowrap text-sm'>
                                {site.qps.toFixed(site.qps >= 10 ? 0 : 2)}
                            </td>
                            <td className='text-sm text-muted'>{site.certificate_count}</td>
                            <td className='whitespace-nowrap text-sm text-muted'>
                                {new Date(site.updated_at).toLocaleString()}
                            </td>
                            <td>
                                <div className='flex justify-end gap-1'>
                                    {canOperate && (
                                        <>
                                            <Button
                                                isIconOnly
                                                aria-label={`Copy ${site.name}`}
                                                size='sm'
                                                variant='ghost'
                                                onPress={() => void cloneSite(site)}
                                            >
                                                <Copy className='h-3.5 w-3.5' />
                                            </Button>
                                            <Button
                                                isIconOnly
                                                aria-label={`Save ${site.name} as template`}
                                                size='sm'
                                                variant='ghost'
                                                onPress={() => void saveTemplate(site)}
                                            >
                                                <FileStack className='h-3.5 w-3.5' />
                                            </Button>
                                        </>
                                    )}
                                    <Button
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => navigate(`/sites/${site.id}/overview`)}
                                    >
                                        <Eye className='mr-1.5 h-3.5 w-3.5' /> View
                                    </Button>
                                </div>
                            </td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
        </div>
    );
}
