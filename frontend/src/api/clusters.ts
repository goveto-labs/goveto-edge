import type { ClusterGroup, ClusterMember, ClusterRegion, DNSLine } from './types.ts';

import { get, post } from './client.ts';

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
