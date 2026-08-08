import type {
    ClusterChoice,
    ClusterGroup,
    ClusterListResponse,
    ClusterMember,
    ClusterRegion,
    DNSLine,
    NotificationChannel,
    NotificationChannelInput,
} from './types.ts';

import { del, get, post, put } from './client.ts';

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

    notificationChannels: () =>
        get<NotificationChannel[]>(clusterPath(clusterId, '/notification-channels')),
    createNotificationChannel: (payload: NotificationChannelInput) =>
        post<NotificationChannel>(clusterPath(clusterId, '/notification-channels'), payload),
    updateNotificationChannel: (channelId: string, payload: NotificationChannelInput) =>
        put<NotificationChannel>(
            clusterPath(clusterId, `/notification-channels/${channelId}`),
            payload
        ),
    deleteNotificationChannel: (channelId: string) =>
        del(clusterPath(clusterId, `/notification-channels/${channelId}`)),
    testNotificationChannel: (channelId: string) =>
        post<{ delivered: boolean }>(
            clusterPath(clusterId, `/notification-channels/${channelId}/test`)
        ),
    testDraftNotificationChannel: (payload: { url: string; name?: string }) =>
        post<{ delivered: boolean }>(
            clusterPath(clusterId, '/notification-channels/test'),
            payload
        ),
});
