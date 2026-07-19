import { useEffect } from 'react';

export const AUTO_REFRESH_INTERVAL = 60_000;

export function useAutoRefresh(
    refresh: () => unknown | Promise<unknown>,
    enabled = true,
    interval = AUTO_REFRESH_INTERVAL
) {
    useEffect(() => {
        if (!enabled) return;

        let inFlight = false;
        const run = async () => {
            if (inFlight) return;
            inFlight = true;
            try {
                await refresh();
            } finally {
                inFlight = false;
            }
        };

        void run();
        const timer = window.setInterval(() => void run(), interval);
        return () => window.clearInterval(timer);
    }, [enabled, interval, refresh]);
}
