import type {
    BulkSiteResult,
    CachePolicy,
    CompressionPolicy,
    CreateSiteRequest,
    DeliveryPolicy,
    SecurityPolicy,
    SiteBundle,
    SiteCacheUpdateResponse,
    SiteCompressionUpdateResponse,
    SiteCreateResponse,
    SiteDeliveryUpdateResponse,
    SiteDetails,
    SiteListenerConfig,
    SiteListenerUpdateResponse,
    SiteSecurityUpdateResponse,
    SiteSummary,
    SiteTemplate,
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
    getCompression: (siteId: string) =>
        get<CompressionPolicy>(clusterPath(clusterId, `/sites/${siteId}/compression`)),
    updateCompression: (siteId: string, payload: CompressionPolicy) =>
        put<SiteCompressionUpdateResponse>(
            clusterPath(clusterId, `/sites/${siteId}/compression`),
            payload
        ),
    getSecurity: (siteId: string) =>
        get<SecurityPolicy>(clusterPath(clusterId, `/sites/${siteId}/security`)),
    updateSecurity: (siteId: string, payload: SecurityPolicy) =>
        put<SiteSecurityUpdateResponse>(
            clusterPath(clusterId, `/sites/${siteId}/security`),
            payload
        ),
    getDelivery: (siteId: string) =>
        get<DeliveryPolicy>(clusterPath(clusterId, `/sites/${siteId}/delivery`)),
    updateDelivery: (siteId: string, payload: DeliveryPolicy) =>
        put<SiteDeliveryUpdateResponse>(
            clusterPath(clusterId, `/sites/${siteId}/delivery`),
            payload
        ),
    export: (siteId: string) => get<SiteBundle>(clusterPath(clusterId, `/sites/${siteId}/export`)),
    clone: (siteId: string, payload: { name: string; domains: string[] }) =>
        post<SiteCreateResponse>(clusterPath(clusterId, `/sites/${siteId}/clone`), payload),
    import: (sites: SiteBundle[]) =>
        post<BulkSiteResult[]>(clusterPath(clusterId, '/sites/import'), { sites }),
    bulk: (payload: {
        site_ids: string[];
        action: 'ENABLE' | 'DISABLE' | 'PUBLISH' | 'SET_DELIVERY';
        delivery?: DeliveryPolicy;
    }) => post<BulkSiteResult[]>(clusterPath(clusterId, '/sites/bulk'), payload),
    listTemplates: () => get<SiteTemplate[]>(clusterPath(clusterId, '/site-templates')),
    getTemplate: (templateId: string) =>
        get<SiteTemplate>(clusterPath(clusterId, `/site-templates/${templateId}`)),
    createTemplate: (payload: { name: string; site_id?: string; config?: SiteBundle }) =>
        post<SiteTemplate>(clusterPath(clusterId, '/site-templates'), payload),
    deleteTemplate: (templateId: string) =>
        del(clusterPath(clusterId, `/site-templates/${templateId}`)),
});
