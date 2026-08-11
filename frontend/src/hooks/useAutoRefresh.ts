import { useCallback, useEffect, useRef, useState } from 'react';

export const AUTO_REFRESH_INTERVAL = 60_000;

export function useAutoRefresh(
    refresh: () => unknown | Promise<unknown>,
    enabled = true,
    interval = AUTO_REFRESH_INTERVAL
) {
    const refreshRef = useRef(refresh);
    refreshRef.current = refresh;
    const [isRefreshing, setIsRefreshing] = useState(false);
    const [error, setError] = useState<Error | null>(null);
    const [lastSuccessfulAt, setLastSuccessfulAt] = useState<Date | null>(null);

    const retry = useCallback(async () => {
        setIsRefreshing(true);
        try {
            await refreshRef.current();
            setLastSuccessfulAt(new Date());
            setError(null);
        } catch (refreshError) {
            setError(refreshError instanceof Error ? refreshError : new Error('Refresh failed'));
        } finally {
            setIsRefreshing(false);
        }
    }, []);

    useEffect(() => {
        if (!enabled) return;
        refreshRef.current = refresh;

        let inFlight = false;
        const run = async () => {
            if (inFlight) return;
            inFlight = true;
            try {
                await retry();
            } finally {
                inFlight = false;
            }
        };

        void run();
        const timer = window.setInterval(() => void run(), interval);
        return () => window.clearInterval(timer);
    }, [enabled, interval, refresh, retry]);

    return {
        error,
        isRefreshing,
        lastSuccessfulAt,
        retry,
        stale: Boolean(error && lastSuccessfulAt),
    };
}
