import type { DeliveryPolicy } from '@/api';

export function defaultDeliveryPolicy(): DeliveryPolicy {
    return {
        request_headers: [],
        response_headers: [],
        rewrites: [],
        redirects: [],
        cors: {
            enabled: false,
            allow_origins: [],
            allow_methods: ['GET', 'HEAD', 'POST', 'OPTIONS'],
            allow_headers: [],
            expose_headers: [],
            allow_credentials: false,
            max_age_seconds: 0,
        },
        protocols: { websocket: true, grpc: false, http_upgrade: false },
        error_pages: [],
        origin_prefix: '',
        origin_pools: [],
        splits: [],
        maintenance: {
            enabled: false,
            status: 503,
            content_type: 'text/html; charset=utf-8',
            body: 'Service temporarily unavailable',
        },
    };
}

export function normalizeDeliveryPolicy(
    value: Partial<DeliveryPolicy> | null | undefined
): DeliveryPolicy {
    const defaults = defaultDeliveryPolicy();
    const cors = value?.cors as Partial<DeliveryPolicy['cors']> | undefined;
    const protocols = value?.protocols as Partial<DeliveryPolicy['protocols']> | undefined;
    const maintenance = value?.maintenance as Partial<DeliveryPolicy['maintenance']> | undefined;

    return {
        request_headers: value?.request_headers ?? [],
        response_headers: value?.response_headers ?? [],
        rewrites: value?.rewrites ?? [],
        redirects: value?.redirects ?? [],
        cors: {
            ...defaults.cors,
            ...cors,
            allow_origins: cors?.allow_origins ?? [],
            allow_methods: cors?.allow_methods ?? defaults.cors.allow_methods,
            allow_headers: cors?.allow_headers ?? [],
            expose_headers: cors?.expose_headers ?? [],
        },
        protocols: { ...defaults.protocols, ...protocols },
        error_pages: value?.error_pages ?? [],
        origin_prefix: value?.origin_prefix ?? '',
        origin_pools: value?.origin_pools ?? [],
        splits: value?.splits ?? [],
        maintenance: { ...defaults.maintenance, ...maintenance },
    };
}
