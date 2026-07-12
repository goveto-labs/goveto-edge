import type {
    DNSConfigResponse,
    DNSLine,
    DNSManagedRecord,
    DNSProviderType,
    DNSSyncJob,
} from './types.ts';

import { del, get, post, put } from './client.ts';

export interface UpdateDNSConfig {
    primary_hostname: string;
    provider: DNSProviderType;
    zone: string;
    zone_id?: string;
    credentials?: Record<string, string>;
    default_ttl: number;
    proxied: boolean;
    enabled: boolean;
}

export const dnsApi = (clusterId: string) => {
    const base = `/clusters/${clusterId}/dns`;
    return {
        config: () => get<DNSConfigResponse>(base),
        update: (payload: UpdateDNSConfig) =>
            put<{ primary_hostname: string; sync_job: DNSSyncJob }>(base, payload),
        disable: () => del<DNSSyncJob>(base),
        records: () => get<DNSManagedRecord[]>(`${base}/records`),
        jobs: () => get<DNSSyncJob[]>(`${base}/jobs`),
        sync: () => post<DNSSyncJob>(`${base}/sync`),
        createLine: (payload: { name: string; provider_code: string }) =>
            post<DNSLine>(`${base}/lines`, payload),
        deleteLine: (lineId: string) => del(`${base}/lines/${lineId}`),
    };
};
