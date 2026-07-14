import type { CachePolicy, CreateSiteRequest, SiteCacheUpdateResponse, SiteCreateResponse, SiteListenerConfig, SiteListenerUpdateResponse } from './types.ts';

import { get, post, put } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const sitesApi = (clusterId: string) => ({
    create: (payload: CreateSiteRequest) => post<SiteCreateResponse>(clusterPath(clusterId, '/sites'), payload),
    getListener: (siteId: string) =>
        get<SiteListenerConfig>(clusterPath(clusterId, `/sites/${siteId}/listener`)),
    updateListener: (siteId: string, payload: Partial<SiteListenerConfig>) =>
        put<SiteListenerUpdateResponse>(
            clusterPath(clusterId, `/sites/${siteId}/listener`),
            payload
        ),
    getCache: (siteId: string) =>
        get<CachePolicy>(clusterPath(clusterId, `/sites/${siteId}/cache`)),
    updateCache: (siteId: string, payload: CachePolicy) =>
        put<SiteCacheUpdateResponse>(
            clusterPath(clusterId, `/sites/${siteId}/cache`),
            payload
        ),
});
