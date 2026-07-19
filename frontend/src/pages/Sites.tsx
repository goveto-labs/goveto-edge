import type { CachePolicy, SiteListenerConfig, SiteSummary } from '@/api';

import { Button, Card, Input, Label, Switch, Tabs } from '@heroui/react';
import { ArrowLeft, Eye, Globe2, Plus, Rocket, Save } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';

import { ApiError, dnsApi, publishApi, sitesApi } from '@/api';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

export default function Sites() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const api = useMemo(() => sitesApi(clusterId), [clusterId]);
    const pubApi = useMemo(() => publishApi(clusterId), [clusterId]);
    const dns = useMemo(() => dnsApi(clusterId), [clusterId]);
    const [searchParams, setSearchParams] = useSearchParams();
    const siteId = searchParams.get('siteId') ?? '';
    const [sites, setSites] = useState<SiteSummary[]>([]);
    const [listener, setListener] = useState<SiteListenerConfig>({});
    const [cache, setCache] = useState<CachePolicy>({});
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');
    const [saveLoading, setSaveLoading] = useState(false);
    const [publishLoading, setPublishLoading] = useState(false);
    const [cnameTarget, setCnameTarget] = useState<string | null>(null);

    const selectedSite = useMemo(
        () => sites.find((site) => site.id === siteId) ?? null,
        [siteId, sites]
    );

    const loadSites = useCallback(async () => {
        if (!clusterId) return;
        setLoading(true);
        try {
            setSites(await api.list());
            setError('');
        } catch (loadError) {
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load sites');
        } finally {
            setLoading(false);
        }
    }, [api, clusterId]);

    useEffect(() => {
        void loadSites();
    }, [loadSites]);

    useEffect(() => {
        if (!clusterId) return;
        setCnameTarget(null);
        dns.config()
            .then((config) => setCnameTarget(config.primary_hostname))
            .catch(() => setCnameTarget(null));
    }, [clusterId, dns]);

    useEffect(() => {
        if (!clusterId || !siteId) return;
        Promise.all([api.getListener(siteId), api.getCache(siteId)])
            .then(([listenerData, cacheData]) => {
                setListener(listenerData);
                setCache(cacheData);
                setError('');
            })
            .catch((loadError) => {
                setError(
                    loadError instanceof ApiError
                        ? loadError.message
                        : 'Failed to load site configuration'
                );
            });
    }, [api, clusterId, siteId]);

    const saveListener = async () => {
        if (!siteId) return;
        setSaveLoading(true);
        setError('');
        try {
            const result = await api.updateListener(siteId, listener);
            setListener(result.listener);
        } catch (saveError) {
            setError(
                saveError instanceof ApiError ? saveError.message : 'Failed to update listener'
            );
        } finally {
            setSaveLoading(false);
        }
    };

    const saveCache = async () => {
        if (!siteId) return;
        setSaveLoading(true);
        setError('');
        try {
            const result = await api.updateCache(siteId, cache);
            setCache(result.cache);
        } catch (saveError) {
            setError(saveError instanceof ApiError ? saveError.message : 'Failed to update cache');
        } finally {
            setSaveLoading(false);
        }
    };

    const publish = async () => {
        if (!siteId) return;
        setPublishLoading(true);
        setError('');
        try {
            await pubApi.enqueueSite(siteId);
        } catch (publishError) {
            setError(publishError instanceof ApiError ? publishError.message : 'Failed to publish');
        } finally {
            setPublishLoading(false);
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Configure site listeners, origins, and cache policies.'
                    title='Sites'
                />
                <Card className='p-8 text-center text-sm text-muted'>
                    Select a cluster in the header to manage sites.
                </Card>
            </div>
        );
    }

    if (!siteId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Manage domains, origins, certificates, and publishing.'
                    title='Sites'
                >
                    <Button onPress={() => navigate('/sites/create')}>
                        <Plus className='mr-2 h-4 w-4' />
                        Create site
                    </Button>
                </PageHeader>

                {error && (
                    <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                        {error}
                    </div>
                )}

                <DataTable
                    aria-label='Sites'
                    empty={sites.length === 0}
                    emptyAction={
                        <Button onPress={() => navigate('/sites/create')}>
                            <Plus className='mr-2 h-4 w-4' />
                            Create site
                        </Button>
                    }
                    emptyDescription='Create a site to route domains to origin servers.'
                    emptyTitle='No sites yet'
                    loading={loading}
                >
                    <thead>
                        <tr>
                            <th>Name</th>
                            <th>Domains</th>
                            <th>Status</th>
                            <th>Certificates</th>
                            <th>Version</th>
                            <th>Updated</th>
                            <th className='text-right'>Actions</th>
                        </tr>
                    </thead>
                    <tbody>
                        {sites.map((site) => (
                            <tr key={site.id}>
                                <td>
                                    <button
                                        className='flex items-center gap-2 text-sm font-semibold hover:underline'
                                        type='button'
                                        onClick={() => setSearchParams({ siteId: site.id })}
                                    >
                                        <Globe2 className='h-4 w-4 text-muted' />
                                        {site.name}
                                    </button>
                                </td>
                                <td className='max-w-sm'>
                                    <span className='line-clamp-2 text-sm text-muted'>
                                        {site.domains.join(', ') || '—'}
                                    </span>
                                </td>
                                <td>
                                    <StatusBadge status={site.status} />
                                </td>
                                <td className='text-sm text-muted'>{site.certificate_count}</td>
                                <td className='text-sm text-muted'>v{site.version}</td>
                                <td className='whitespace-nowrap text-sm text-muted'>
                                    {new Date(site.updated_at).toLocaleString()}
                                </td>
                                <td>
                                    <div className='flex justify-end'>
                                        <Button
                                            size='sm'
                                            variant='secondary'
                                            onPress={() => setSearchParams({ siteId: site.id })}
                                        >
                                            <Eye className='mr-1.5 h-3.5 w-3.5' />
                                            View
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

    return (
        <div className='space-y-6'>
            <PageHeader
                actions={
                    <Button variant='ghost' onPress={() => setSearchParams({})}>
                        <ArrowLeft className='mr-1.5 h-4 w-4' />
                        Back to sites
                    </Button>
                }
                subtitle={selectedSite?.domains.join(', ') || `ID: ${siteId}`}
                title={selectedSite?.name ?? 'Site configuration'}
            >
                <Button isDisabled={publishLoading} onPress={() => void publish()}>
                    <Rocket className='mr-2 h-4 w-4' />
                    {publishLoading ? 'Publishing…' : 'Publish'}
                </Button>
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <Card className='p-5'>
                <div className='flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between'>
                    <div className='min-w-0'>
                        <div className='flex items-center gap-2 text-sm font-medium'>
                            <Globe2 className='h-4 w-4 text-muted' />
                            CNAME target
                        </div>
                        {cnameTarget ? (
                            <>
                                <p className='mt-1 text-sm text-muted'>
                                    At your DNS provider, point every Site domain to this CDN
                                    hostname. For an apex domain, use ALIAS or CNAME flattening if
                                    required by the provider.
                                </p>
                                <code className='mt-3 block w-fit max-w-full overflow-x-auto rounded-lg bg-surface-secondary px-3 py-2 text-sm font-medium text-foreground'>
                                    {cnameTarget}
                                </code>
                            </>
                        ) : (
                            <p className='mt-1 text-sm text-muted'>
                                Configure a cluster primary hostname before routing customer
                                domains.
                            </p>
                        )}
                    </div>
                    {!cnameTarget && (
                        <Button size='sm' variant='secondary' onPress={() => navigate('/dns')}>
                            Configure DNS
                        </Button>
                    )}
                </div>
            </Card>

            <Tabs>
                <Tabs.List>
                    <Tabs.Tab id='listener'>Listener</Tabs.Tab>
                    <Tabs.Tab id='cache'>Cache</Tabs.Tab>
                </Tabs.List>
                <Tabs.Panel className='space-y-4 pt-4' id='listener'>
                    <Card className='p-5'>
                        <div className='mb-4 text-sm font-medium'>Listener settings</div>
                        <div className='grid grid-cols-1 gap-4 sm:grid-cols-2'>
                            <div className='flex flex-col gap-1'>
                                <Label htmlFor='listener-http-port'>HTTP port</Label>
                                <Input
                                    id='listener-http-port'
                                    type='number'
                                    value={String(listener.http_port ?? 80)}
                                    variant='secondary'
                                    onChange={(event) =>
                                        setListener({
                                            ...listener,
                                            http_port: Number(event.target.value),
                                        })
                                    }
                                />
                            </div>
                            <div className='flex flex-col gap-1'>
                                <Label htmlFor='listener-https-port'>HTTPS port</Label>
                                <Input
                                    id='listener-https-port'
                                    type='number'
                                    value={String(listener.https_port ?? 443)}
                                    variant='secondary'
                                    onChange={(event) =>
                                        setListener({
                                            ...listener,
                                            https_port: Number(event.target.value),
                                        })
                                    }
                                />
                            </div>
                        </div>
                        <div className='mt-5 flex flex-wrap gap-4'>
                            <label className='flex items-center gap-2 text-sm' htmlFor='site-http2'>
                                <Switch
                                    id='site-http2'
                                    isSelected={!!listener.http2_enabled}
                                    onChange={(checked) =>
                                        setListener({ ...listener, http2_enabled: checked })
                                    }
                                />
                                HTTP/2
                            </label>
                            <label className='flex items-center gap-2 text-sm' htmlFor='site-http3'>
                                <Switch
                                    id='site-http3'
                                    isSelected={!!listener.http3_enabled}
                                    onChange={(checked) =>
                                        setListener({ ...listener, http3_enabled: checked })
                                    }
                                />
                                HTTP/3
                            </label>
                            <label
                                className='flex items-center gap-2 text-sm'
                                htmlFor='site-redirect-http'
                            >
                                <Switch
                                    id='site-redirect-http'
                                    isSelected={!!listener.redirect_http_to_https}
                                    onChange={(checked) =>
                                        setListener({
                                            ...listener,
                                            redirect_http_to_https: checked,
                                        })
                                    }
                                />
                                Redirect HTTP to HTTPS
                            </label>
                        </div>
                        <Button
                            className='mt-5'
                            isDisabled={saveLoading}
                            onPress={() => void saveListener()}
                        >
                            <Save className='mr-2 h-4 w-4' />
                            {saveLoading ? 'Saving…' : 'Save listener'}
                        </Button>
                    </Card>
                </Tabs.Panel>
                <Tabs.Panel className='space-y-4 pt-4' id='cache'>
                    <Card className='p-5'>
                        <div className='mb-4 text-sm font-medium'>Cache policy</div>
                        <label
                            className='flex items-center gap-2 text-sm'
                            htmlFor='site-cache-enabled'
                        >
                            <Switch
                                id='site-cache-enabled'
                                isSelected={!!cache.enabled}
                                onChange={(checked) => setCache({ ...cache, enabled: checked })}
                            />
                            Enable caching
                        </label>
                        <div className='mt-5 grid grid-cols-1 gap-4 sm:grid-cols-2'>
                            <div className='flex flex-col gap-1'>
                                <Label htmlFor='cache-ttl'>TTL seconds</Label>
                                <Input
                                    id='cache-ttl'
                                    type='number'
                                    value={String(cache.ttl?.default_seconds ?? 0)}
                                    variant='secondary'
                                    onChange={(event) =>
                                        setCache({
                                            ...cache,
                                            ttl: {
                                                ...(cache.ttl ?? {}),
                                                default_seconds: Number(event.target.value),
                                            },
                                        })
                                    }
                                />
                            </div>
                            <div className='flex flex-col gap-1'>
                                <Label htmlFor='cache-stale'>Stale if error seconds</Label>
                                <Input
                                    id='cache-stale'
                                    type='number'
                                    value={String(cache.stale?.if_error_seconds ?? 0)}
                                    variant='secondary'
                                    onChange={(event) =>
                                        setCache({
                                            ...cache,
                                            stale: {
                                                ...(cache.stale ?? {}),
                                                if_error_seconds: Number(event.target.value),
                                            },
                                        })
                                    }
                                />
                            </div>
                        </div>
                        <Button
                            className='mt-5'
                            isDisabled={saveLoading}
                            onPress={() => void saveCache()}
                        >
                            <Save className='mr-2 h-4 w-4' />
                            {saveLoading ? 'Saving…' : 'Save cache'}
                        </Button>
                    </Card>
                </Tabs.Panel>
            </Tabs>
        </div>
    );
}
