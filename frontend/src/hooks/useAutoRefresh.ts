import { useCallback, useEffect, useRef, useState } from 'react';

import { RequestGeneration } from '@/hooks/requestGeneration.ts';

export const AUTO_REFRESH_INTERVAL = 60_000;

export function useAutoRefresh(
    refresh: (signal: AbortSignal) => unknown | Promise<unknown>,
    enabled = true,
    interval = AUTO_REFRESH_INTERVAL
) {
    const refreshRef = useRef(refresh);
    refreshRef.current = refresh;
    const [isRefreshing, setIsRefreshing] = useState(false);
    const [error, setError] = useState<Error | null>(null);
    const [lastSuccessfulAt, setLastSuccessfulAt] = useState<Date | null>(null);
    const generationRef = useRef<RequestGeneration | null>(null);
    const controllerRef = useRef<AbortController | null>(null);
    if (!generationRef.current) generationRef.current = new RequestGeneration();

    const retry = useCallback(async () => {
        const generation = generationRef.current!;
        const request = generation.next();
        controllerRef.current?.abort();
        const controller = new AbortController();
        controllerRef.current = controller;
        setIsRefreshing(true);
        try {
            await refreshRef.current(controller.signal);
            if (!generation.isCurrent(request, controller.signal)) return;
            setLastSuccessfulAt(new Date());
            setError(null);
        } catch (refreshError) {
            if (!generation.isCurrent(request, controller.signal)) return;
            setError(refreshError instanceof Error ? refreshError : new Error('Refresh failed'));
        } finally {
            if (generation.isCurrent(request, controller.signal)) {
                controllerRef.current = null;
                setIsRefreshing(false);
            }
        }
    }, []);

    useEffect(() => {
        if (!enabled) {
            generationRef.current?.invalidate();
            controllerRef.current?.abort();
            controllerRef.current = null;
            setIsRefreshing(false);
            return;
        }
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
        return () => {
            window.clearInterval(timer);
            generationRef.current?.invalidate();
            controllerRef.current?.abort();
            controllerRef.current = null;
        };
    }, [enabled, interval, refresh, retry]);

    return {
        error,
        isRefreshing,
        lastSuccessfulAt,
        retry,
        stale: Boolean(error && lastSuccessfulAt),
    };
}
