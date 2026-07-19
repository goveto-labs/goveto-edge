import type { PrewarmResult, PurgeJob, PurgeType, SiteSummary } from '@/api';

import { Button, Input, TextArea } from '@heroui/react';
import { Eraser, Flame, Globe2, Link2, RefreshCw, Tags } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import { ApiError, purgeApi, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';

const purgeOptions: Array<{
    id: PurgeType;
    label: string;
    description: string;
    placeholder: string;
    inputLabel: string;
    icon: typeof Link2;
}> = [
    {
        id: 'URL',
        label: 'Exact URL',
        description: 'Remove one cached URL without affecting related paths.',
        placeholder: 'https://example.com/assets/app.css',
        inputLabel: 'URL to refresh',
        icon: Link2,
    },
    {
        id: 'PREFIX',
        label: 'Path prefix',
        description: 'Remove every cached response below a path.',
        placeholder: '/assets/',
        inputLabel: 'Path prefix',
        icon: Globe2,
    },
    {
        id: 'TAG',
        label: 'Cache tag',
        description: 'Remove responses associated with one surrogate key.',
        placeholder: 'product:1234',
        inputLabel: 'Cache tag',
        icon: Tags,
    },
    {
        id: 'ALL',
        label: 'Entire site',
        description: 'Remove all cached responses for the selected site.',
        placeholder: '',
        inputLabel: '',
        icon: Eraser,
    },
];

type FixedSite = Pick<SiteSummary, 'id' | 'name' | 'domains'>;

export function CacheOperations({
    fixedSite,
    embedded = false,
}: {
    fixedSite?: FixedSite;
    embedded?: boolean;
}) {
    const { clusterId } = useCluster();
    const api = useMemo(() => purgeApi(clusterId), [clusterId]);
    const sites = useMemo(() => sitesApi(clusterId), [clusterId]);
    const [searchParams, setSearchParams] = useSearchParams();
    const siteId = fixedSite?.id ?? searchParams.get('siteId') ?? '';

    const [jobs, setJobs] = useState<PurgeJob[]>([]);
    const [siteItems, setSiteItems] = useState<SiteSummary[]>([]);
    const [type, setType] = useState<PurgeType>('URL');
    const [value, setValue] = useState('');
    const [loading, setLoading] = useState(false);
    const [submitting, setSubmitting] = useState(false);
    const [prewarmURLs, setPrewarmURLs] = useState('');
    const [prewarming, setPrewarming] = useState(false);
    const [prewarmResults, setPrewarmResults] = useState<PrewarmResult[]>([]);
    const [error, setError] = useState('');
    const [message, setMessage] = useState('');

    const selectedSite = fixedSite ?? siteItems.find((site) => site.id === siteId);
    const selectedPurge = purgeOptions.find((option) => option.id === type) ?? purgeOptions[0];
    const prewarmURLList = Array.from(
        new Set(
            prewarmURLs
                .split(/\r?\n/)
                .map((item) => item.trim())
                .filter(Boolean)
        )
    );
    const prewarmCount = prewarmURLList.length;

    const load = useCallback(async () => {
        if (!clusterId || !siteId) {
            setJobs([]);
            return;
        }
        setLoading(true);
        try {
            setJobs(await api.list(siteId));
            setError('');
        } catch (loadError) {
            setError(
                loadError instanceof ApiError ? loadError.message : 'Failed to load cache jobs'
            );
        } finally {
            setLoading(false);
        }
    }, [api, clusterId, siteId]);

    const loadSites = useCallback(async () => {
        if (!clusterId || fixedSite) return;
        try {
            const items = await sites.list();
            setSiteItems(items);
            if (!siteId && items[0]) setSearchParams({ siteId: items[0].id }, { replace: true });
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load sites');
        }
    }, [clusterId, fixedSite, setSearchParams, siteId, sites]);

    useAutoRefresh(loadSites, Boolean(clusterId && !fixedSite));

    const handleSiteChange = (nextSiteId: string) => {
        setMessage('');
        setPrewarmResults([]);
        setSearchParams(nextSiteId ? { siteId: nextSiteId } : {}, { replace: true });
    };

    useAutoRefresh(load, Boolean(clusterId && siteId));

    const handlePurge = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!siteId || (type !== 'ALL' && !value.trim())) return;
        setSubmitting(true);
        setError('');
        setMessage('');
        try {
            const job = await api.enqueue(siteId, {
                type,
                value: type === 'ALL' ? undefined : value.trim(),
            });
            setJobs((current) => [job, ...current]);
            setValue('');
            setMessage(`${selectedPurge.label} refresh was queued.`);
        } catch (submitError) {
            setError(
                submitError instanceof ApiError
                    ? submitError.message
                    : 'Failed to enqueue cache refresh'
            );
        } finally {
            setSubmitting(false);
        }
    };

    const handlePrewarm = async (event: React.FormEvent) => {
        event.preventDefault();
        const urls = prewarmURLList;
        if (!siteId || urls.length === 0 || urls.length > 20) return;
        setPrewarming(true);
        setError('');
        setMessage('');
        try {
            const results = await api.prewarm(siteId, urls);
            setPrewarmResults(results);
            setMessage(
                `${results.filter((result) => result.success).length} of ${results.length} URLs were prewarmed.`
            );
        } catch (prewarmError) {
            setError(
                prewarmError instanceof ApiError ? prewarmError.message : 'Failed to prewarm URLs'
            );
        } finally {
            setPrewarming(false);
        }
    };

    const addHomepage = () => {
        const domain = selectedSite?.domains?.[0];
        if (!domain) return;
        const homepage = `https://${domain}/`;
        const current = prewarmURLs.trim();
        setPrewarmURLs(current ? `${current}\n${homepage}` : homepage);
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Refresh cached content and prewarm site URLs.'
                    title='Cache operations'
                />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to manage site caches.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            {!embedded && (
                <PageHeader
                    subtitle='Refresh cached responses and prepare frequently requested URLs.'
                    title='Cache operations'
                >
                    {!fixedSite && (
                        <select
                            aria-label='Site'
                            className='w-full min-w-0 rounded-lg border border-border bg-surface px-3 py-2 text-sm sm:min-w-64'
                            value={siteId}
                            onChange={(event) => handleSiteChange(event.target.value)}
                        >
                            {siteItems.length === 0 && <option value=''>No sites available</option>}
                            {siteItems.map((site) => (
                                <option key={site.id} value={site.id}>
                                    {site.name} ({site.domains?.[0] || site.id})
                                </option>
                            ))}
                        </select>
                    )}
                </PageHeader>
            )}

            {error && (
                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                    {error}
                </div>
            )}
            {message && (
                <div className='rounded-lg border border-success/20 bg-success/10 px-4 py-3 text-sm text-success'>
                    {message}
                </div>
            )}

            <ContentCard noPadding>
                <div className='border-b border-border px-5 py-4'>
                    <h2 className='text-sm font-semibold'>Refresh cached content</h2>
                    <p className='mt-1 text-xs leading-5 text-muted'>
                        Choose the smallest scope that contains the content you need to invalidate.
                    </p>
                </div>
                <form onSubmit={handlePurge}>
                    <div className='space-y-5 p-5'>
                        <div className='grid gap-3 sm:grid-cols-2'>
                            {purgeOptions.map((option) => {
                                const Icon = option.icon;
                                const selected = type === option.id;
                                return (
                                    <button
                                        aria-pressed={selected}
                                        className={`flex items-start gap-3 rounded-xl border px-4 py-3.5 text-left transition-colors ${
                                            selected
                                                ? 'border-primary bg-primary/5'
                                                : 'border-border/70 hover:bg-surface-secondary/50'
                                        }`}
                                        key={option.id}
                                        type='button'
                                        onClick={() => {
                                            setType(option.id);
                                            setValue('');
                                        }}
                                    >
                                        <span
                                            className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ${
                                                selected
                                                    ? 'bg-primary text-primary-foreground'
                                                    : 'bg-surface-secondary text-muted'
                                            }`}
                                        >
                                            <Icon className='h-4 w-4' />
                                        </span>
                                        <span>
                                            <span className='block text-sm font-medium'>
                                                {option.label}
                                            </span>
                                            <span className='mt-0.5 block text-xs leading-5 text-muted'>
                                                {option.description}
                                            </span>
                                        </span>
                                    </button>
                                );
                            })}
                        </div>

                        {type === 'ALL' ? (
                            <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-xs leading-5 text-danger'>
                                This refreshes the entire site cache. New requests will repopulate
                                content from the origin.
                            </div>
                        ) : (
                            <div className='flex flex-col gap-1.5'>
                                <label className='text-sm font-medium' htmlFor='purge-value'>
                                    {selectedPurge.inputLabel}
                                </label>
                                <Input
                                    id='purge-value'
                                    placeholder={selectedPurge.placeholder}
                                    value={value}
                                    variant='secondary'
                                    onChange={(event) => setValue(event.target.value)}
                                />
                            </div>
                        )}
                    </div>
                    <div className='flex flex-col gap-3 border-t border-border bg-surface-secondary/20 px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                        <div className='text-xs text-muted'>
                            {selectedSite
                                ? `Target: ${selectedSite.name}`
                                : 'Select a site before refreshing cache.'}
                        </div>
                        <Button
                            isDisabled={submitting || !siteId || (type !== 'ALL' && !value.trim())}
                            type='submit'
                            variant={type === 'ALL' ? 'danger' : 'primary'}
                        >
                            <Eraser className='mr-1.5 h-4 w-4' />
                            {submitting
                                ? 'Queuing...'
                                : type === 'ALL'
                                  ? 'Refresh entire site'
                                  : 'Queue refresh'}
                        </Button>
                    </div>
                </form>
            </ContentCard>

            <ContentCard noPadding>
                <div className='flex flex-col gap-3 border-b border-border px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                    <div>
                        <h2 className='text-sm font-semibold'>Prewarm URLs</h2>
                        <p className='mt-1 text-xs leading-5 text-muted'>
                            Request important pages now so the first visitor receives cached
                            content.
                        </p>
                    </div>
                    <Button
                        isDisabled={!selectedSite?.domains?.[0]}
                        size='sm'
                        variant='secondary'
                        onPress={addHomepage}
                    >
                        Add homepage
                    </Button>
                </div>
                <form onSubmit={handlePrewarm}>
                    <div className='space-y-3 p-5'>
                        <TextArea
                            aria-label='URLs to prewarm'
                            placeholder={'https://example.com/\nhttps://example.com/assets/app.css'}
                            className={'w-full'}
                            rows={6}
                            value={prewarmURLs}
                            variant='secondary'
                            onChange={(event) => setPrewarmURLs(event.target.value)}
                        />
                        <div className='flex items-center justify-between gap-4 text-xs'>
                            <span className={prewarmCount > 20 ? 'text-danger' : 'text-muted'}>
                                {prewarmCount}/20 URLs
                            </span>
                            <span className='text-muted'>One absolute URL per line.</span>
                        </div>
                    </div>
                    <div className='flex justify-end border-t border-border bg-surface-secondary/20 px-5 py-4'>
                        <Button
                            isDisabled={
                                prewarming || !siteId || prewarmCount === 0 || prewarmCount > 20
                            }
                            type='submit'
                        >
                            <Flame className='mr-1.5 h-4 w-4' />
                            {prewarming ? 'Prewarming...' : `Prewarm ${prewarmCount || ''}`.trim()}
                        </Button>
                    </div>
                </form>
                {prewarmResults.length > 0 && (
                    <div className='border-t border-border px-5 py-4'>
                        <div className='mb-3 text-xs font-medium text-muted'>Latest result</div>
                        <div className='space-y-2'>
                            {prewarmResults.map((result) => (
                                <div
                                    className='flex flex-col gap-1 rounded-lg bg-surface-secondary/40 px-3 py-2 text-xs sm:flex-row sm:items-center sm:justify-between sm:gap-4'
                                    key={result.url}
                                >
                                    <span className='min-w-0 break-all font-mono'>
                                        {result.url}
                                    </span>
                                    <span
                                        className={`shrink-0 font-medium ${result.success ? 'text-success' : 'text-danger'}`}
                                    >
                                        {result.success
                                            ? `HTTP ${result.status_code}`
                                            : result.error || `HTTP ${result.status_code || 0}`}
                                    </span>
                                </div>
                            ))}
                        </div>
                    </div>
                )}
            </ContentCard>

            <DataTable
                aria-label='Cache refresh jobs'
                action={
                    <Button
                        isDisabled={!siteId || loading}
                        size='sm'
                        variant='secondary'
                        onPress={load}
                    >
                        <RefreshCw className='mr-1.5 h-3.5 w-3.5' />
                        Refresh
                    </Button>
                }
                empty={jobs.length === 0}
                emptyDescription='Queued cache refresh operations will appear here.'
                emptyTitle='No cache refresh jobs'
                loading={loading && jobs.length === 0}
                title='Recent refresh jobs'
            >
                <thead>
                    <tr>
                        <th>Scope</th>
                        <th>Target</th>
                        <th>Status</th>
                        <th>Created</th>
                        <th>Job ID</th>
                    </tr>
                </thead>
                <tbody>
                    {jobs.map((job) => (
                        <tr key={job.id}>
                            <td className='text-sm font-medium'>
                                {purgeOptions.find((option) => option.id === job.type)?.label ||
                                    job.type}
                            </td>
                            <td className='max-w-lg font-mono text-xs text-muted'>
                                {job.value ?? 'All cached content'}
                            </td>
                            <td>
                                <StatusBadge status={job.status} />
                            </td>
                            <td className='whitespace-nowrap text-sm text-muted'>
                                {new Date(job.created_at).toLocaleString()}
                            </td>
                            <td className='font-mono text-xs text-muted'>{job.id}</td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
        </div>
    );
}

export default function PurgeJobs() {
    return <CacheOperations />;
}
