import type {
    CachePolicy,
    CreateSiteRequest,
    SecurityPolicy,
    SiteCacheUpdateResponse,
    SiteCreateResponse,
    SiteDetails,
    SiteListenerConfig,
    SiteListenerUpdateResponse,
    SiteSecurityUpdateResponse,
    SiteSummary,
    UpdateSiteRequest,
} from './types.ts';

import { del, get, patch, post, put } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const sitesApi = (clusterId: string) => ({
    list: () => get<SiteSummary[]>(clusterPath(clusterId, '/sites')),
    get: (siteId: string) => get<SiteDetails>(clusterPath(clusterId, `/sites/${siteId}`)),
    update: (siteId: string, payload: UpdateSiteRequest) =>
        patch<SiteDetails>(clusterPath(clusterId, `/sites/${siteId}`), payload),
    delete: (siteId: string) => del(clusterPath(clusterId, `/sites/${siteId}`)),
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
    getSecurity: (siteId: string) =>
        get<SecurityPolicy>(clusterPath(clusterId, `/sites/${siteId}/security`)),
    updateSecurity: (siteId: string, payload: SecurityPolicy) =>
        put<SiteSecurityUpdateResponse>(
            clusterPath(clusterId, `/sites/${siteId}/security`),
            payload
        ),
});
