import type { TrafficPoint } from '@/api';

const hourMs = 3_600_000;
const dayMs = 86_400_000;

/**
 * Fill missing buckets with zero-valued points so trend charts always render
 * a continuous series, even when no traffic was recorded.
 */
export function fillTrafficSeries(series: TrafficPoint[], period: '24h' | '30d'): TrafficPoint[] {
    const step = period === '24h' ? hourMs : dayMs;
    const count = period === '24h' ? 24 : 30;
    const byBucket = new Map<number, TrafficPoint>();
    for (const point of series) {
        const key = Math.floor(new Date(point.bucket).getTime() / step) * step;
        byBucket.set(key, point);
    }
    const end = Math.floor(Date.now() / step) * step;
    const filled: TrafficPoint[] = [];
    for (let index = count - 1; index >= 0; index--) {
        const bucket = end - index * step;
        filled.push(
            byBucket.get(bucket) ?? {
                bucket: new Date(bucket).toISOString(),
                requests: 0,
                ingress_bytes: 0,
                egress_bytes: 0,
                cache_egress_bytes: 0,
            }
        );
    }
    return filled;
}
