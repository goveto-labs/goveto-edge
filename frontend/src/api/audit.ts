import type { AuditEventList } from './types.ts';

import { buildQuery, get } from './client.ts';

export interface AuditQuery extends Record<string, string | number | boolean | undefined> {
    actor?: string;
    action?: string;
    resource_type?: string;
    resource_id?: string;
    request_id?: string;
    result?: string;
    from?: string;
    to?: string;
    page?: number;
    page_size?: number;
}

export const auditApi = {
    list: (query: AuditQuery) => get<AuditEventList>(`/audit/events${buildQuery(query)}`),
};
