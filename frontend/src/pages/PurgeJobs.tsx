import type { PrewarmResult, PurgeJob, SiteSummary } from '@/api';

import { Button, TextArea } from '@heroui/react';
import { Flame, RefreshCw } from 'lucide-react';
import { useCallback, useMemo, useRef, useState } from 'react';

import { ApiError, purgeApi, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { canOperateCluster } from '@/utils/rbac.ts';

type OperationMode = 'refresh' | 'prewarm';
type FixedSite = Pick<SiteSummary, 'id' | 'name' | 'domains'>;

interface MatchedURL {
    line: number;
    site: FixedSite;
    url: string;
}

interface URLIssue {
    line: number;
    input: string;
    message: string;
}

const operationTabs = [
    { id: 'refresh', label: 'Refresh' },
    { id: 'prewarm', label: 'Prewarm' },
];

function normalizedHostname(value: string) {
    return value.trim().toLowerCase().replace(/\.$/, '');
}

export function matchSiteURLs(input: string, sites: FixedSite[]) {
    const matched: MatchedURL[] = [];
    const issues: URLIssue[] = [];
    const seen = new Set<string>();

    for (const [index, rawLine] of input.split(/\r?\n/).entries()) {
        const value = rawLine.trim();
        if (!value) continue;

        let parsed: URL;
        try {
            parsed = new URL(value);
        } catch {
            issues.push({ line: index + 1, input: value, message: 'Enter an absolute URL.' });
            continue;
        }

        if (
            !['http:', 'https:'].includes(parsed.protocol) ||
            !parsed.hostname ||
            parsed.username ||
            parsed.password
        ) {
            issues.push({
                line: index + 1,
                input: value,
                message: 'Use an HTTP or HTTPS URL without credentials.',
            });
            continue;
        }

        parsed.hash = '';
        const normalizedURL = parsed.toString();
        if (seen.has(normalizedURL)) continue;
        seen.add(normalizedURL);

        const hostname = normalizedHostname(parsed.hostname);
        const candidates = sites.filter((site) =>
            site.domains.some((domain) => normalizedHostname(domain) === hostname)
        );
        if (candidates.length === 0) {
            issues.push({
                line: index + 1,
                input: value,
                message: `No site in this cluster owns ${parsed.hostname}.`,
            });
            continue;
        }
        if (candidates.length > 1) {
            issues.push({
                line: index + 1,
                input: value,
                message: `More than one site owns ${parsed.hostname}.`,
            });
            continue;
        }
        matched.push({ line: index + 1, site: candidates[0], url: normalizedURL });
    }

    return { issues, matched };
}

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError ? error.message : fallback;
}

function chunks<T>(items: T[], size: number) {
    const result: T[][] = [];
    for (let index = 0; index < items.length; index += size) {
        result.push(items.slice(index, index + size));
    }
    return result;
}

export function CacheOperations({
    fixedSite,
    embedded = false,
}: {
    fixedSite?: FixedSite;
    embedded?: boolean;
}) {
    const { clusterId, clusters } = useCluster();
    const canOperate = canOperateCluster(
        clusters.find((cluster) => cluster.id === clusterId)?.role
    );
    const api = useMemo(() => purgeApi(clusterId), [clusterId]);
    const sites = useMemo(() => sitesApi(clusterId), [clusterId]);
    const [mode, setMode] = useState<OperationMode>('refresh');
    const [urls, setURLs] = useState('');
    const [jobs, setJobs] = useState<PurgeJob[]>([]);
    const [siteItems, setSiteItems] = useState<SiteSummary[]>([]);
    const [loadedClusterID, setLoadedClusterID] = useState('');
    const [prewarmResults, setPrewarmResults] = useState<PrewarmResult[]>([]);
    const [loading, setLoading] = useState(false);
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState('');
    const [message, setMessage] = useState('');
    const activeClusterID = useRef(clusterId);
    const loadedClusterIDRef = useRef('');
    activeClusterID.current = clusterId;

    const availableSites = useMemo<FixedSite[]>(
        () => (fixedSite ? [fixedSite] : loadedClusterID === clusterId ? siteItems : []),
        [clusterId, fixedSite, loadedClusterID, siteItems]
    );
    const sitesReady = Boolean(fixedSite || loadedClusterID === clusterId);
    const parsedURLs = useMemo(
        () => (sitesReady ? matchSiteURLs(urls, availableSites) : { issues: [], matched: [] }),
        [availableSites, sitesReady, urls]
    );
    const lineCount = urls.split(/\r?\n/).filter((line) => line.trim()).length;
    const matchedSiteCount = new Set(parsedURLs.matched.map((target) => target.site.id)).size;

    const loadSites = useCallback(async () => {
        if (!clusterId || fixedSite) return;
        const requestedClusterID = clusterId;
        if (loadedClusterIDRef.current !== requestedClusterID) setSiteItems([]);
        try {
            const items = await sites.list();
            if (activeClusterID.current !== requestedClusterID) return;
            setSiteItems(items);
        } catch (loadError) {
            if (activeClusterID.current !== requestedClusterID) return;
            setError(errorMessage(loadError, 'Failed to load sites'));
        } finally {
            if (activeClusterID.current === requestedClusterID) {
                loadedClusterIDRef.current = requestedClusterID;
                setLoadedClusterID(requestedClusterID);
            }
        }
    }, [clusterId, fixedSite, sites]);

    useAutoRefresh(loadSites, Boolean(clusterId && !fixedSite));

    const loadJobs = useCallback(async () => {
        if (!clusterId || availableSites.length === 0) {
            setJobs([]);
            return;
        }
        setLoading(true);
        try {
            const results = await Promise.allSettled(
                availableSites.map((site) => api.list(site.id))
            );
            const loaded = results
                .flatMap((result) => (result.status === 'fulfilled' ? result.value : []))
                .sort(
                    (left, right) =>
                        new Date(right.created_at).getTime() - new Date(left.created_at).getTime()
                );
            setJobs(loaded);
            const failedCount = results.filter((result) => result.status === 'rejected').length;
            setError(
                failedCount > 0 ? `Refresh history is unavailable for ${failedCount} site(s).` : ''
            );
        } finally {
            setLoading(false);
        }
    }, [api, availableSites, clusterId]);

    useAutoRefresh(loadJobs, Boolean(clusterId && availableSites.length > 0));

    const submitRefresh = async (targets: MatchedURL[]) => {
        const results = await Promise.allSettled(
            targets.map((target) => api.enqueue(target.site.id, { type: 'URL', value: target.url }))
        );
        const queued = results.flatMap((result) =>
            result.status === 'fulfilled' ? [result.value] : []
        );
        setJobs((current) => [...queued, ...current]);
        const failed = results.length - queued.length;
        if (failed > 0) {
            setError(`${failed} URL refresh request(s) could not be queued.`);
        }
        setMessage(`${queued.length} of ${results.length} URL refresh request(s) queued.`);
    };

    const submitPrewarm = async (targets: MatchedURL[]) => {
        const bySite = new Map<string, string[]>();
        for (const target of targets) {
            const current = bySite.get(target.site.id) ?? [];
            current.push(target.url);
            bySite.set(target.site.id, current);
        }
        const batches = Array.from(bySite, ([siteId, siteURLs]) =>
            chunks(siteURLs, 20).map((batch) => ({ siteId, urls: batch }))
        ).flat();
        const settled = await Promise.allSettled(
            batches.map((batch) => api.prewarm(batch.siteId, batch.urls))
        );
        const results = settled.flatMap((result, index) => {
            if (result.status === 'fulfilled') return result.value;
            const failure = errorMessage(result.reason, 'Prewarm request failed');
            return batches[index].urls.map((url) => ({ url, success: false, error: failure }));
        });
        setPrewarmResults(results);
        const succeeded = results.filter((result) => result.success).length;
        const failed = results.length - succeeded;
        if (failed > 0) setError(`${failed} URL(s) could not be prewarmed.`);
        setMessage(`${succeeded} of ${results.length} URL(s) prewarmed.`);
    };

    const handleSubmit = async (event: React.FormEvent) => {
        event.preventDefault();
        if (
            !canOperate ||
            parsedURLs.matched.length === 0 ||
            parsedURLs.issues.length > 0 ||
            submitting
        )
            return;

        setSubmitting(true);
        setError('');
        setMessage('');
        setPrewarmResults([]);
        try {
            if (mode === 'refresh') await submitRefresh(parsedURLs.matched);
            else await submitPrewarm(parsedURLs.matched);
        } catch (submitError) {
            setError(
                errorMessage(
                    submitError,
                    mode === 'refresh' ? 'Failed to queue URL refreshes' : 'Failed to prewarm URLs'
                )
            );
        } finally {
            setSubmitting(false);
        }
    };

    const siteName = (siteID: string) =>
        availableSites.find((site) => site.id === siteID)?.name ?? siteID;

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='Refresh or prewarm exact URLs.' title='Cache operations' />
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to manage site caches.
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-4'>
            {!embedded && (
                <PageHeader
                    activeTab={mode}
                    subtitle='Sites are matched automatically from each URL hostname.'
                    tabs={operationTabs}
                    title='Cache operations'
                    onTabChange={(tab) => {
                        setMode(tab as OperationMode);
                        setError('');
                        setMessage('');
                        setPrewarmResults([]);
                    }}
                />
            )}

            {embedded && (
                <div className='flex w-fit gap-1 rounded-xl bg-surface p-1' role='tablist'>
                    {operationTabs.map((tab) => (
                        <button
                            aria-selected={mode === tab.id}
                            className={`rounded-lg px-4 py-1.5 text-sm font-medium ${mode === tab.id ? 'bg-surface-secondary shadow-sm' : 'text-muted'}`}
                            key={tab.id}
                            role='tab'
                            type='button'
                            onClick={() => setMode(tab.id as OperationMode)}
                        >
                            {tab.label}
                        </button>
                    ))}
                </div>
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

            {canOperate ? (
                <ContentCard noPadding>
                    <form onSubmit={handleSubmit}>
                        <div className='space-y-4 p-5'>
                            <FormField
                                htmlFor='cache-operation-urls'
                                hint='One absolute HTTP or HTTPS URL per line. Query strings are preserved; fragments are ignored.'
                                label={mode === 'refresh' ? 'URLs to refresh' : 'URLs to prewarm'}
                                required
                            >
                                <TextArea
                                    id='cache-operation-urls'
                                    placeholder={
                                        'https://www.example.com/assets/app.css\nhttps://shop.example.net/products/42'
                                    }
                                    rows={8}
                                    value={urls}
                                    variant='secondary'
                                    onChange={(event) => {
                                        setURLs(event.target.value);
                                        setError('');
                                        setMessage('');
                                    }}
                                />
                            </FormField>

                            {parsedURLs.issues.length > 0 && (
                                <div aria-live='polite' className='space-y-1 text-xs text-danger'>
                                    {parsedURLs.issues.map((issue) => (
                                        <div key={`${issue.line}-${issue.input}`}>
                                            Line {issue.line}: {issue.message}
                                        </div>
                                    ))}
                                </div>
                            )}
                        </div>
                        <div className='flex flex-col gap-3 border-t border-border bg-surface-secondary/20 px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                            <div className='text-xs text-muted'>
                                {!sitesReady
                                    ? 'Loading site domains...'
                                    : lineCount === 0
                                      ? 'Enter at least one URL.'
                                      : `${parsedURLs.matched.length} URL(s) matched across ${matchedSiteCount} site(s).`}
                            </div>
                            <Button
                                isDisabled={
                                    submitting ||
                                    !sitesReady ||
                                    parsedURLs.matched.length === 0 ||
                                    parsedURLs.issues.length > 0
                                }
                                type='submit'
                            >
                                {mode === 'refresh' ? (
                                    <RefreshCw className='h-4 w-4' />
                                ) : (
                                    <Flame className='h-4 w-4' />
                                )}
                                {submitting
                                    ? mode === 'refresh'
                                        ? 'Queuing...'
                                        : 'Prewarming...'
                                    : mode === 'refresh'
                                      ? `Refresh ${parsedURLs.matched.length || ''}`.trim()
                                      : `Prewarm ${parsedURLs.matched.length || ''}`.trim()}
                            </Button>
                        </div>
                    </form>

                    {prewarmResults.length > 0 && (
                        <div className='border-t border-border px-5 py-4'>
                            <h2 className='mb-3 text-xs font-medium text-muted'>Latest result</h2>
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
            ) : (
                <ContentCard className='p-8 text-center text-sm text-muted'>
                    Your cluster role does not allow cache operations.
                </ContentCard>
            )}

            <DataTable
                aria-label='Cache refresh jobs'
                action={
                    <Button isDisabled={loading} size='sm' variant='secondary' onPress={loadJobs}>
                        <RefreshCw className='h-3.5 w-3.5' />
                        Refresh
                    </Button>
                }
                empty={jobs.length === 0}
                emptyDescription='Queued URL refresh operations will appear here.'
                emptyTitle='No URL refresh jobs'
                loading={loading && jobs.length === 0}
                title='Recent refresh jobs'
            >
                <thead>
                    <tr>
                        <th>Site</th>
                        <th>URL</th>
                        <th>Status</th>
                        <th>Created</th>
                        <th>Job ID</th>
                    </tr>
                </thead>
                <tbody>
                    {jobs.map((job) => (
                        <tr key={job.id}>
                            <td className='text-sm font-medium'>{siteName(job.site_id)}</td>
                            <td className='max-w-lg break-all font-mono text-xs text-muted'>
                                {job.value ?? '-'}
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
