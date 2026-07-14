import type {
    ClusterChoice,
    ClusterGroup,
    ClusterListResponse,
    ClusterMember,
    ClusterRegion,
    DNSLine,
} from './types.ts';

import { get, post, put } from './client.ts';

export const clustersApi = {
    list: () => get<ClusterListResponse>('/clusters'),
    create: (name: string) =>
        post<{ cluster: ClusterChoice; selected_cluster_id: string }>('/clusters', { name }),
    select: (clusterId: string) =>
        put<{ selected_cluster_id: string }>('/session/cluster', { cluster_id: clusterId }),
};

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const clusterApi = (clusterId: string) => ({
    dnsLines: () => get<DNSLine[]>(clusterPath(clusterId, '/dns-lines')),

    groups: () => get<ClusterGroup[]>(clusterPath(clusterId, '/groups')),
    createGroup: (name: string) => post<ClusterGroup>(clusterPath(clusterId, '/groups'), { name }),

    regions: () => get<ClusterRegion[]>(clusterPath(clusterId, '/regions')),
    createRegion: (name: string) =>
        post<ClusterRegion>(clusterPath(clusterId, '/regions'), { name }),

    members: () => get<ClusterMember[]>(clusterPath(clusterId, '/members')),
    addMember: (payload: { user_id: string; permission: string }) =>
        post<ClusterMember>(clusterPath(clusterId, '/members'), payload),
});
