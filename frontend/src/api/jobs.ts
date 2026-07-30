import type { JobExecution, ManagedJob, ManagedJobKind, ManagedJobPage } from './types.ts';

import { buildQuery, get, post } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const jobsApi = (clusterId: string) => ({
    list: (
        filters: {
            kind?: string;
            status?: string;
            query?: string;
            page?: number;
            page_size?: number;
        } = {}
    ) => get<ManagedJobPage>(clusterPath(clusterId, `/jobs${buildQuery(filters)}`)),
    detail: (kind: ManagedJobKind, jobId: string) =>
        get<ManagedJob>(clusterPath(clusterId, `/jobs/${kind}/${jobId}`)),
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
