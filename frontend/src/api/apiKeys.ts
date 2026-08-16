import type {
    ApiKeyPermission,
    ApiKeyStatus,
    ClusterApiKey,
    ClusterApiKeyCreated,
    ClusterApiKeyList,
} from './types.ts';

import { buildQuery, del, get, patch, post } from './client.ts';

export interface ApiKeyCreateInput {
    name: string;
    permissions: ApiKeyPermission[];
    expires_at?: string;
    expires_in_days?: number;
}

export interface ApiKeyUpdateInput {
    name?: string;
    permissions?: ApiKeyPermission[];
    status?: ApiKeyStatus;
    expires_at?: string;
    expires_in_days?: number;
    clear_expiry?: boolean;
}

export interface ApiKeyQuery extends Record<string, string | number | boolean | undefined> {
    status?: ApiKeyStatus;
    page?: number;
    page_size?: number;
}

export const apiKeysApi = (clusterId: string) => {
    const base = `/clusters/${clusterId}/api-keys`;
    return {
        list: (query: ApiKeyQuery = {}) => get<ClusterApiKeyList>(`${base}${buildQuery(query)}`),
        create: (payload: ApiKeyCreateInput) => post<ClusterApiKeyCreated>(base, payload),
        update: (keyId: string, payload: ApiKeyUpdateInput) =>
            patch<ClusterApiKey>(`${base}/${keyId}`, payload),
        rotate: (keyId: string, graceSeconds = 600) =>
            post<ClusterApiKeyCreated>(`${base}/${keyId}/rotate`, {
                grace_seconds: graceSeconds,
            }),
        revoke: (keyId: string) => del<ClusterApiKey>(`${base}/${keyId}`),
    };
};
