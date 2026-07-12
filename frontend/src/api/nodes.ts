import type { CreateNodeRequest, Node, NodeAddress, NodeCacheConfig } from './types.ts';

import { del, get, post, put } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const nodesApi = (clusterId: string) => ({
    list: () => get<Node[]>(clusterPath(clusterId, '/nodes')),
    get: (nodeId: string) => get<Node>(clusterPath(clusterId, `/nodes/${nodeId}`)),
    create: (payload: CreateNodeRequest) =>
        post<{ id: string; status: string }>(clusterPath(clusterId, '/nodes'), payload),
    delete: (nodeId: string) => del<void>(clusterPath(clusterId, `/nodes/${nodeId}`)),

    getCacheConfig: (nodeId: string) =>
        get<NodeCacheConfig>(clusterPath(clusterId, `/nodes/${nodeId}/cache-config`)),
    updateCacheConfig: (nodeId: string, payload: NodeCacheConfig) =>
        put<{ cache_config: NodeCacheConfig; synced: boolean; sync_error?: string }>(
            clusterPath(clusterId, `/nodes/${nodeId}/cache-config`),
            payload
        ),

    addAddress: (nodeId: string, payload: { address: string; primary: boolean }) =>
        post<NodeAddress>(clusterPath(clusterId, `/nodes/${nodeId}/addresses`), payload),
});
