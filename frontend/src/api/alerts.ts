import type { AxiosRequestConfig } from 'axios';

import { buildQuery, get, post, put } from './client.ts';

export type AlertStatus = 'PENDING' | 'FIRING' | 'ACKNOWLEDGED' | 'RESOLVED';
export type AlertSeverity = 'INFO' | 'WARNING' | 'CRITICAL';
export type AlertEventType = 'STATE_CHANGE' | 'NOTIFY' | 'ACK' | 'RESOLVE' | 'REARM';
export type AlertDeliveryStatus = 'PENDING' | 'SENT' | 'FAILED';

export interface AlertInstance {
    id: string;
    ruleId: string;
    clusterId: string;
    kind: string;
    status: AlertStatus;
    severity: AlertSeverity;
    title: string;
    detailJson: Record<string, unknown>;
    firstSeenAt: string;
    lastSeenAt: string;
    firedAt?: string | null;
    ackedAt?: string | null;
    ackedBy?: string | null;
    resolvedAt?: string | null;
    resolvedReason?: string | null;
    notifyCount: number;
    lastNotifiedAt?: string | null;
    createdAt: string;
    updatedAt: string;
}

export interface AlertEvent {
    id: string;
    type: AlertEventType;
    fromStatus?: string | null;
    toStatus?: string | null;
    payloadJson: Record<string, unknown>;
    createdAt: string;
}

export interface AlertDelivery {
    id: string;
    channelId: string;
    channelName: string;
    kind: 'firing' | 'recovery';
    status: AlertDeliveryStatus;
    attempts: number;
    lastError?: string | null;
    nextRetryAt?: string | null;
    sentAt?: string | null;
    createdAt: string;
}

export type AlertDetail = AlertInstance;

export interface AlertHistoryPage<T> {
    items: T[];
    page: number;
    page_size: number;
    total: number;
}

export interface AlertPage {
    items: AlertInstance[];
    page: number;
    page_size: number;
    total: number;
}

export interface AlertListFilters {
    status?: AlertStatus;
    severity?: AlertSeverity;
    kind?: string;
    active?: boolean;
    page?: number;
    page_size?: number;
}

export interface AlertRule {
    id: string;
    clusterId: string;
    kind: string;
    label: string;
    description: string;
    enabled: boolean;
    severity: AlertSeverity;
    paramsJson: Record<string, number | string | boolean>;
    parameterSpecs: AlertRuleParameterSpec[];
    forSeconds: number;
    cooldownSeconds: number;
    mutedUntil: string | null;
    channels: string[];
    createdAt: string;
    updatedAt: string;
}

export interface AlertRuleParameterSpec {
    key: string;
    label: string;
    type: 'integer' | 'number';
    default: number;
    minimum: number;
    maximum: number;
    exclusiveMinimum: boolean;
    step?: number;
}

export interface AlertRuleUpdate {
    enabled?: boolean;
    severity?: AlertSeverity;
    params?: Record<string, number | null>;
    forSeconds?: number;
    cooldownSeconds?: number;
    mutedUntil?: string;
    unmute?: boolean;
    channels?: string[];
}

export interface AlertOverviewCluster {
    clusterId: string;
    name: string;
    firing: number;
}

export interface AlertOverviewItem {
    id: string;
    clusterId: string;
    kind: string;
    title: string;
    severity: AlertSeverity;
    status: AlertStatus;
    lastSeenAt: string;
}

export interface AlertOverview {
    clusters: AlertOverviewCluster[];
    firingTotal: number;
    recent: AlertOverviewItem[];
}

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const alertsApi = (clusterId: string) => ({
    list: (filters: AlertListFilters = {}) =>
        get<AlertPage>(clusterPath(clusterId, `/alerts${buildQuery({ ...filters })}`)),
    detail: (alertId: string, config?: AxiosRequestConfig) =>
        get<AlertDetail>(clusterPath(clusterId, `/alerts/${alertId}`), config),
    events: (alertId: string, page = 1, pageSize = 50, config?: AxiosRequestConfig) =>
        get<AlertHistoryPage<AlertEvent>>(
            clusterPath(
                clusterId,
                `/alerts/${alertId}/events${buildQuery({ page, page_size: pageSize })}`
            ),
            config
        ),
    deliveries: (alertId: string, page = 1, pageSize = 50, config?: AxiosRequestConfig) =>
        get<AlertHistoryPage<AlertDelivery>>(
            clusterPath(
                clusterId,
                `/alerts/${alertId}/deliveries${buildQuery({ page, page_size: pageSize })}`
            ),
            config
        ),
    ack: (alertId: string) => post<AlertInstance>(clusterPath(clusterId, `/alerts/${alertId}/ack`)),
    resolve: (alertId: string, reason?: string) =>
        post<AlertInstance>(clusterPath(clusterId, `/alerts/${alertId}/resolve`), { reason }),
    batchAck: (ids: string[]) =>
        post<{ acknowledged: number }>(clusterPath(clusterId, '/alerts/batch-ack'), { ids }),
    rules: () => get<AlertRule[]>(clusterPath(clusterId, '/alert-rules')),
    updateRule: (ruleId: string, body: AlertRuleUpdate) =>
        put<AlertRule>(clusterPath(clusterId, `/alert-rules/${ruleId}`), body),
});

export const alertOverviewApi = {
    overview: (config?: Parameters<typeof get<AlertOverview>>[1]) =>
        get<AlertOverview>('/alerts/overview', config),
};
