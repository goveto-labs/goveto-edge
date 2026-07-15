import type {
    CachePolicy,
    CreateSiteRequest,
    SiteCacheUpdateResponse,
    SiteCreateResponse,
    SiteListenerConfig,
    SiteListenerUpdateResponse,
    SiteSummary,
} from './types.ts';

import { get, patch, post, put } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const sitesApi = (clusterId: string) => ({
    list: () => get<SiteSummary[]>(clusterPath(clusterId, '/sites')),
    create: (payload: CreateSiteRequest) =>
        post<SiteCreateResponse>(clusterPath(clusterId, '/sites'), payload),
    getListener: (siteId: string) =>
        get<SiteListenerConfig>(clusterPath(clusterId, `/sites/${siteId}/listener`)),
    updateListener: (siteId: string, payload: Partial<SiteListenerConfig>) =>
        patch<SiteListenerUpdateResponse>(
            clusterPath(clusterId, `/sites/${siteId}/listener`),
            payload
        ),
    getCache: (siteId: string) =>
        get<CachePolicy>(clusterPath(clusterId, `/sites/${siteId}/cache`)),
    updateCache: (siteId: string, payload: CachePolicy) =>
        put<SiteCacheUpdateResponse>(clusterPath(clusterId, `/sites/${siteId}/cache`), payload),
});
