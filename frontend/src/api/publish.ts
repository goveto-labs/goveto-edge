import type { PublishJob, PublishStatus } from './types.ts';

import { API_BASE, get, post } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const publishApi = (clusterId: string) => ({
    status: () => get<PublishStatus>(clusterPath(clusterId, '/publish/status')),
    events: () =>
        new EventSource(`${API_BASE}${clusterPath(clusterId, '/publish/events')}`, {
            withCredentials: true,
        }),
    enqueueSite: (siteId: string) =>
        post<PublishJob>(clusterPath(clusterId, `/sites/${siteId}/publish`)),
    listSiteJobs: (siteId: string) =>
        get<PublishJob[]>(clusterPath(clusterId, `/sites/${siteId}/publish`)),
    getSiteJob: (siteId: string, jobId: string) =>
        get<PublishJob>(clusterPath(clusterId, `/sites/${siteId}/publish/${jobId}`)),
});
