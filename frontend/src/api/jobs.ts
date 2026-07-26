import type { JobExecution, ManagedJob, ManagedJobKind } from './types.ts';

import { buildQuery, get, post } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const jobsApi = (clusterId: string) => ({
    list: (filters: { kind?: string; status?: string } = {}) =>
        get<ManagedJob[]>(clusterPath(clusterId, `/jobs${buildQuery(filters)}`)),
    executions: (kind: ManagedJobKind, jobId: string) =>
        get<JobExecution[]>(clusterPath(clusterId, `/jobs/${kind}/${jobId}/executions`)),
    cancel: (kind: ManagedJobKind, jobId: string) =>
        post<{ id: string; status: string }>(
            clusterPath(clusterId, `/jobs/${kind}/${jobId}/cancel`)
        ),
    replay: (kind: ManagedJobKind, jobId: string) =>
        post<{ id: string; status: string }>(
            clusterPath(clusterId, `/jobs/${kind}/${jobId}/replay`)
        ),
});
