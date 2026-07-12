import type {
    AnalyticsParams,
    DistributionItem,
    NodeRuntimeResponse,
    Summary,
    TopItem,
    TrafficResponse,
} from './types.ts';

import { get } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const analyticsApi = (clusterId: string) => ({
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
    rankings: (dimension: string, params: AnalyticsParams) =>
        get<DistributionItem[]>(
            clusterPath(
                clusterId,
                `/analytics/rankings/${dimension}${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    distributions: (dimension: string, params: AnalyticsParams) =>
        get<DistributionItem[]>(
            clusterPath(
                clusterId,
                `/analytics/distributions/${dimension}${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
            )
        ),
    nodeRuntime: (params: { node_id?: string; period?: string }) =>
        get<NodeRuntimeResponse>(
            clusterPath(
                clusterId,
                `/analytics/nodes/runtime${buildQuery(params as unknown as Record<string, string | number | boolean | undefined>)}`
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
