import type {
    AnalyticsParams,
    DistributionItem,
    MonitoringOverview,
    NodeRequestLog,
    NodeRuntimeResponse,
    NodeSnapshot,
    RequestLogPage,
    Summary,
    TopItem,
    TrafficResponse,
    WAFRuleStat,
} from './types.ts';

import { get } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const analyticsApi = (clusterId: string) => ({
    overview: (params: Pick<AnalyticsParams, 'site_id'> = {}) =>
        get<MonitoringOverview>(
            clusterPath(
                clusterId,
                `/analytics/overview${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    summary: (params: AnalyticsParams) =>
        get<Summary>(
            clusterPath(
                clusterId,
                `/analytics/summary${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    topUrls: (params: AnalyticsParams & { limit?: number }) =>
        get<TopItem[]>(
            clusterPath(
                clusterId,
                `/analytics/top-urls${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    topIps: (params: AnalyticsParams & { limit?: number }) =>
        get<TopItem[]>(
            clusterPath(
                clusterId,
                `/analytics/top-ips${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    traffic: (params: AnalyticsParams & { period: '24h' | '30d' }) =>
        get<TrafficResponse>(
            clusterPath(
                clusterId,
                `/analytics/traffic${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    rankings: (
        dimension: string,
        params: AnalyticsParams & {
            period?: '24h' | '30d';
            sort?: 'requests' | 'traffic';
            limit?: number;
        }
    ) =>
        get<DistributionItem[]>(
            clusterPath(
                clusterId,
                `/analytics/rankings/${dimension}${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    distributions: (
        dimension: string,
        params: AnalyticsParams & {
            period?: '24h' | '30d';
            sort?: 'requests' | 'traffic';
            limit?: number;
        }
    ) =>
        get<DistributionItem[]>(
            clusterPath(
                clusterId,
                `/analytics/distributions/${dimension}${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    nodeRuntime: (params: { node_id?: string; period?: '12h' | '24h' | '30d' }) =>
        get<NodeRuntimeResponse>(
            clusterPath(
                clusterId,
                `/analytics/nodes/runtime${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    latestNodeRuntime: (nodeId?: string) =>
        get<NodeSnapshot[]>(
            clusterPath(
                clusterId,
                `/analytics/nodes/runtime/latest${buildQuery({ node_id: nodeId })}`
            )
        ),
    nodeLogs: (nodeId: string, limit = 100) =>
        get<NodeRequestLog[]>(
            clusterPath(clusterId, `/analytics/nodes/logs${buildQuery({ node_id: nodeId, limit })}`)
        ),
    siteLogs: (
        params: { site_id?: string; page?: number; page_size?: number; query?: string } = {}
    ) =>
        get<RequestLogPage>(
            clusterPath(clusterId, `/analytics/sites/logs${buildQuery(params)}`)
        ),
    wafStats: (siteId: string, from: string, to: string, limit = 100) =>
        get<WAFRuleStat[]>(
            clusterPath(
                clusterId,
                `/analytics/waf${buildQuery({ site_id: siteId, from, to, limit })}`
            )
        ),
});

function buildQuery(params: Record<string, string | number | boolean | undefined>): string {
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
        if (value === undefined || value === '') continue;
        search.set(key, String(value));
    }
    const query = search.toString();
    return query ? `?${query}` : '';
}
