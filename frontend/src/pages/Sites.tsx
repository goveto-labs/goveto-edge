import type {
    CachePolicy,
    Certificate,
    SiteCreateResponse,
    SiteListenerConfig,
    SiteOrigin,
} from '@/api';

import { Button, Card, Input, Label, ListBox, Select, Spinner, Switch, Tabs } from '@heroui/react';
import { Plus, Rocket, RotateCcw, Save } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import { ApiError, certificatesApi, publishApi, sitesApi } from '@/api';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

function parseCommaList(value: string) {
    return value
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
}

const emptyOrigin: SiteOrigin = {
    protocol: 'HTTP',
    address: '',
    host_header: '',
    weight: 1,
};

type OriginFormItem = SiteOrigin & { localId: string };

function createEmptyOrigin(): OriginFormItem {
    const id =
        typeof crypto !== 'undefined' && crypto.randomUUID
            ? crypto.randomUUID()
            : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
    return { ...emptyOrigin, localId: id };
}

export default function Sites() {
    const { clusterId } = useCluster();
    const certsApi = useMemo(() => certificatesApi(clusterId), [clusterId]);
    const siteApi = useMemo(() => sitesApi(clusterId), [clusterId]);
    const pubApi = useMemo(() => publishApi(clusterId), [clusterId]);
    const [searchParams, setSearchParams] = useSearchParams();
    const siteId = searchParams.get('siteId') ?? '';

    const [certs, setCerts] = useState<Certificate[]>([]);
    const [certIds, setCertIds] = useState<Set<string>>(new Set());

    const [createName, setCreateName] = useState('');
    const [createDomains, setCreateDomains] = useState('');
    const [origins, setOrigins] = useState<OriginFormItem[]>([createEmptyOrigin()]);
    const [createLoading, setCreateLoading] = useState(false);
    const [createError, setCreateError] = useState('');

    const [site, setSite] = useState<SiteCreateResponse | null>(null);
    const [listener, setListener] = useState<SiteListenerConfig>({});
    const [cache, setCache] = useState<CachePolicy>({});
    const [detailLoading, setDetailLoading] = useState(false);
    const [detailError, setDetailError] = useState('');
    const [saveLoading, setSaveLoading] = useState(false);
    const [publishLoading, setPublishLoading] = useState(false);

    useEffect(() => {
        if (!clusterId) return;
        certsApi
            .list()
            .then(setCerts)
            .catch((err) =>
                setDetailError(err instanceof ApiError ? err.message : 'Failed to load certs')
            );
    }, [certsApi, clusterId]);

    useEffect(() => {
        if (!clusterId || !siteId) return;
        setDetailLoading(true);
        Promise.all([siteApi.getListener(siteId), siteApi.getCache(siteId)])
            .then(([l, c]) => {
                setListener(l);
                setCache(c);
                setDetailError('');
            })
            .catch((err) => {
                setDetailError(
                    err instanceof ApiError ? err.message : 'Failed to load site config'
                );
            })
            .finally(() => setDetailLoading(false));
    }, [siteApi, clusterId, siteId]);

    const handleCreate = async (e: React.FormEvent) => {
        e.preventDefault();
        setCreateLoading(true);
        setCreateError('');
        try {
            const created = await siteApi.create({
                name: createName,
                domains: parseCommaList(createDomains),
                certificate_ids: Array.from(certIds),
                origins: origins.filter((o) => o.address.trim()).map(({ localId: _, ...o }) => o),
            });
            setSite(created);
            setSearchParams({ siteId: created.id });
        } catch (err) {
            setCreateError(err instanceof ApiError ? err.message : 'Failed to create site');
        } finally {
            setCreateLoading(false);
        }
    };

    const updateOrigin = (index: number, patch: Partial<SiteOrigin>) => {
        setOrigins((prev) => prev.map((o, i) => (i === index ? { ...o, ...patch } : o)));
    };

    const removeOrigin = (index: number) => {
        setOrigins((prev) => prev.filter((_, i) => i !== index));
    };

    const saveListener = async () => {
        if (!siteId) return;
        setSaveLoading(true);
        try {
            const result = await siteApi.updateListener(siteId, listener);
            setListener(result.listener);
        } catch (err) {
            setDetailError(err instanceof ApiError ? err.message : 'Failed to update listener');
        } finally {
            setSaveLoading(false);
        }
    };

    const saveCache = async () => {
        if (!siteId) return;
        setSaveLoading(true);
        try {
            const result = await siteApi.updateCache(siteId, cache);
            setCache(result.cache);
        } catch (err) {
            setDetailError(err instanceof ApiError ? err.message : 'Failed to update cache');
        } finally {
            setSaveLoading(false);
        }
    };

    const publish = async () => {
        if (!siteId) return;
        setPublishLoading(true);
        try {
            await pubApi.enqueueSite(siteId);
        } catch (err) {
            setDetailError(err instanceof ApiError ? err.message : 'Failed to publish');
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
                <Card className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to manage sites.
                    </div>
                </Card>
            </div>
        );
    }

    if (!siteId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Add a new site with origins and certificates.'
                    title='Create site'
                />
                {createError && (
                    <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                        {createError}
                    </div>
                )}
                <Card className='p-5'>
                    <form className='space-y-5' onSubmit={handleCreate}>
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='site-name'>Site name</Label>
                            <Input
                                variant='secondary'
                                id='site-name'
                                required
                                value={createName}
                                onChange={(e) => setCreateName(e.target.value)}
                            />
                        </div>
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='site-domains'>Domains (comma separated)</Label>
                            <Input
                                variant='secondary'
                                id='site-domains'
                                required
                                value={createDomains}
                                onChange={(e) => setCreateDomains(e.target.value)}
                            />
                        </div>
                        <Select
                            variant='secondary'
                            value={Array.from(certIds)}
                            selectionMode='multiple'
                            onChange={(keys) => setCertIds(new Set(keys as string[]))}
                        >
                            <Label>Certificates</Label>
                            <Select.Trigger>
                                <Select.Value />
                            </Select.Trigger>
                            <Select.Popover>
                                <ListBox>
                                    {certs.map((cert) => (
                                        <ListBox.Item
                                            key={cert.id}
                                            id={cert.id}
                                            textValue={cert.name}
                                        >
                                            {cert.name}
                                        </ListBox.Item>
                                    ))}
                                </ListBox>
                            </Select.Popover>
                        </Select>

                        <div className='space-y-3'>
                            <div className='text-sm font-medium'>Origins</div>
                            {origins.map((origin, idx) => (
                                <div key={origin.localId} className='grid grid-cols-12 gap-2'>
                                    <Select
                                        variant='secondary'
                                        className='col-span-2'
                                        aria-label='Origin protocol'
                                        value={origin.protocol}
                                        onChange={(key) =>
                                            updateOrigin(idx, {
                                                protocol: String(key ?? '') as 'HTTP' | 'HTTPS',
                                            })
                                        }
                                    >
                                        <Select.Trigger>
                                            <Select.Value />
                                        </Select.Trigger>
                                        <Select.Popover>
                                            <ListBox>
                                                <ListBox.Item id='HTTP' textValue='HTTP'>
                                                    HTTP
                                                </ListBox.Item>
                                                <ListBox.Item id='HTTPS' textValue='HTTPS'>
                                                    HTTPS
                                                </ListBox.Item>
                                            </ListBox>
                                        </Select.Popover>
                                    </Select>
                                    <Input
                                        variant='secondary'
                                        className='col-span-5'
                                        aria-label='Origin address'
                                        placeholder='Address'
                                        value={origin.address}
                                        onChange={(e) =>
                                            updateOrigin(idx, { address: e.target.value })
                                        }
                                    />
                                    <Input
                                        variant='secondary'
                                        className='col-span-3'
                                        aria-label='Origin host header'
                                        placeholder='Host header'
                                        value={origin.host_header}
                                        onChange={(e) =>
                                            updateOrigin(idx, { host_header: e.target.value })
                                        }
                                    />
                                    <Input
                                        variant='secondary'
                                        className='col-span-1'
                                        aria-label='Origin weight'
                                        placeholder='W'
                                        type='number'
                                        value={String(origin.weight ?? 1)}
                                        onChange={(e) =>
                                            updateOrigin(idx, { weight: Number(e.target.value) })
                                        }
                                    />
                                    <Button
                                        className='col-span-1'
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => removeOrigin(idx)}
                                    >
                                        ×
                                    </Button>
                                </div>
                            ))}
                            <Button
                                size='sm'
                                variant='ghost'
                                onPress={() => setOrigins((prev) => [...prev, createEmptyOrigin()])}
                            >
                                <Plus className='mr-2 h-4 w-4' />
                                Add origin
                            </Button>
                        </div>

                        <Button
                            fullWidth
                            isDisabled={createLoading}
                            type='submit'
                            variant='primary'
                        >
                            {createLoading ? 'Creating...' : 'Create site'}
                        </Button>
                    </form>
                </Card>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader subtitle={`ID: ${siteId}`} title={site?.name ?? 'Site config'}>
                <Button isDisabled={publishLoading} variant='primary' onPress={publish}>
                    <Rocket className='mr-2 h-4 w-4' />
                    {publishLoading ? 'Publishing...' : 'Publish'}
                </Button>
                <Button variant='ghost' onPress={() => setSearchParams({})}>
                    <RotateCcw className='mr-2 h-4 w-4' />
                    New site
                </Button>
            </PageHeader>

            {detailError && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {detailError}
                </div>
            )}

            {detailLoading ? (
                <div className='flex h-64 items-center justify-center'>
                    <Spinner />
                </div>
            ) : (
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
                                        variant='secondary'
                                        id='listener-http-port'
                                        type='number'
                                        value={String(listener.http_port ?? 80)}
                                        onChange={(e) =>
                                            setListener({
                                                ...listener,
                                                http_port: Number(e.target.value),
                                            })
                                        }
                                    />
                                </div>
                                <div className='flex flex-col gap-1'>
                                    <Label htmlFor='listener-https-port'>HTTPS port</Label>
                                    <Input
                                        variant='secondary'
                                        id='listener-https-port'
                                        type='number'
                                        value={String(listener.https_port ?? 443)}
                                        onChange={(e) =>
                                            setListener({
                                                ...listener,
                                                https_port: Number(e.target.value),
                                            })
                                        }
                                    />
                                </div>
                            </div>
                            <div className='mt-5 flex flex-wrap gap-4'>
                                <label
                                    className='flex items-center gap-2 text-sm'
                                    htmlFor='site-http2'
                                >
                                    <Switch
                                        id='site-http2'
                                        isSelected={!!listener.http2_enabled}
                                        onChange={(checked) =>
                                            setListener({ ...listener, http2_enabled: checked })
                                        }
                                    />
                                    HTTP/2
                                </label>
                                <label
                                    className='flex items-center gap-2 text-sm'
                                    htmlFor='site-http3'
                                >
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
                                onPress={saveListener}
                            >
                                <Save className='mr-2 h-4 w-4' />
                                {saveLoading ? 'Saving...' : 'Save listener'}
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
                                        variant='secondary'
                                        id='cache-ttl'
                                        type='number'
                                        value={String(cache.ttl?.default_seconds ?? 0)}
                                        onChange={(e) =>
                                            setCache({
                                                ...cache,
                                                ttl: {
                                                    ...(cache.ttl ?? {}),
                                                    default_seconds: Number(e.target.value),
                                                },
                                            })
                                        }
                                    />
                                </div>
                                <div className='flex flex-col gap-1'>
                                    <Label htmlFor='cache-stale'>
                                        Stale while revalidate seconds
                                    </Label>
                                    <Input
                                        variant='secondary'
                                        id='cache-stale'
                                        type='number'
                                        value={String(cache.stale?.if_error_seconds ?? 0)}
                                        onChange={(e) =>
                                            setCache({
                                                ...cache,
                                                stale: {
                                                    ...(cache.stale ?? {}),
                                                    if_error_seconds: Number(e.target.value),
                                                },
                                            })
                                        }
                                    />
                                </div>
                            </div>
                            <Button className='mt-5' isDisabled={saveLoading} onPress={saveCache}>
                                <Save className='mr-2 h-4 w-4' />
                                {saveLoading ? 'Saving...' : 'Save cache'}
                            </Button>
                        </Card>
                    </Tabs.Panel>
                </Tabs>
            )}
        </div>
    );
}
