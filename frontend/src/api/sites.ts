import type { CachePolicy, CreateSiteRequest, Site, SiteListenerConfig } from './types.ts';

import { get, post, put } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const sitesApi = (clusterId: string) => ({
    create: (payload: CreateSiteRequest) => post<Site>(clusterPath(clusterId, '/sites'), payload),
    getListener: (siteId: string) =>
        get<SiteListenerConfig>(clusterPath(clusterId, `/sites/${siteId}/listener`)),
    updateListener: (siteId: string, payload: Partial<SiteListenerConfig>) =>
        put<{ listener: SiteListenerConfig; publish_job?: string; publish_error?: string }>(
            clusterPath(clusterId, `/sites/${siteId}/listener`),
            payload
        ),
    getCache: (siteId: string) =>
        get<CachePolicy>(clusterPath(clusterId, `/sites/${siteId}/cache`)),
    updateCache: (siteId: string, payload: CachePolicy) =>
        put<{ cache: CachePolicy; publish_job?: string; publish_error?: string }>(
            clusterPath(clusterId, `/sites/${siteId}/cache`),
            payload
        ),
});
