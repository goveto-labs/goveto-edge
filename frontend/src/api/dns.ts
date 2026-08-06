import type {
    DNSConfigResponse,
    DNSManagedRecord,
    DNSProviderDomain,
    DNSProviderType,
    DNSSyncJob,
    DNSZone,
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

export interface DNSDiscoveryRequest {
    provider: DNSProviderType;
    zone?: string;
    zone_id?: string;
    credentials?: Record<string, string>;
    config_id?: string;
}

export interface CreateDNSZoneRequest {
    provider: DNSProviderType;
    zone: string;
    zone_id?: string;
    credentials: Record<string, string>;
    enabled?: boolean;
}

export interface UpdateDNSZoneRequest {
    provider?: DNSProviderType;
    zone?: string;
    zone_id?: string;
    credentials?: Record<string, string>;
    enabled?: boolean;
}

export const dnsApi = (clusterId: string) => {
    const base = `/clusters/${clusterId}/dns`;
    return {
        config: () => get<DNSConfigResponse>(base),
        update: (payload: UpdateDNSConfig) => put<DNSConfigResponse>(base, payload),
        delete: () => del(base),
        refresh: () => post<DNSConfigResponse>(`${base}/refresh`),
        zones: () => get<DNSZone[]>(`${base}/zones`),
        createZone: (payload: CreateDNSZoneRequest) => post<DNSZone>(`${base}/zones`, payload),
        updateZone: (zoneConfigId: string, payload: UpdateDNSZoneRequest) =>
            put<DNSZone>(`${base}/zones/${zoneConfigId}`, payload),
        deleteZone: (zoneConfigId: string) => del(`${base}/zones/${zoneConfigId}`),
        records: () => get<DNSManagedRecord[]>(`${base}/records`),
        jobs: () => get<DNSSyncJob[]>(`${base}/jobs`),
        sync: () => post<DNSSyncJob | null>(`${base}/sync`),
        discoverDomains: (payload: DNSDiscoveryRequest) =>
            post<DNSProviderDomain[]>(`${base}/discovery/domains`, payload),
    };
};
