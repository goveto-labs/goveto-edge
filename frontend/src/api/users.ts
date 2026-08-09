import type { PlatformUser, PlatformUserList, UserStatus } from './types.ts';

import { buildQuery, get, patch } from './client.ts';

export interface UserQuery extends Record<string, string | number | boolean | undefined> {
    search?: string;
    role?: string;
    status?: string;
    page?: number;
    page_size?: number;
}

export const usersApi = {
    list: (query: UserQuery) => get<PlatformUserList>(`/users${buildQuery(query)}`),
    updateStatus: (userId: string, status: UserStatus) =>
        patch<PlatformUser>(`/users/${userId}/status`, { status }),
};
