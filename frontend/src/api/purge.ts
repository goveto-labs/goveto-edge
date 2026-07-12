import type { PurgeJob, PurgeType } from './types.ts';

import { get, post } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const purgeApi = (clusterId: string) => ({
    enqueue: (siteId: string, payload: { type: PurgeType; value?: string }) =>
        post<PurgeJob>(clusterPath(clusterId, `/sites/${siteId}/purge`), payload),
    list: (siteId: string) => get<PurgeJob[]>(clusterPath(clusterId, `/sites/${siteId}/purge`)),
    get: (siteId: string, jobId: string) =>
        get<PurgeJob>(clusterPath(clusterId, `/sites/${siteId}/purge/${jobId}`)),
});
