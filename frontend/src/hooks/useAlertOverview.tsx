import type { AlertOverview } from '@/api';

import {
    createContext,
    useCallback,
    useContext,
    useEffect,
    useMemo,
    useRef,
    useState,
} from 'react';

import { alertOverviewApi } from '@/api';
import { useAuth } from '@/hooks/useAuth.ts';

const REFRESH_INTERVAL = 30_000;

interface AlertOverviewContextValue {
    overview: AlertOverview | null;
    loading: boolean;
    error: string | null;
    updatedAt: Date | null;
    refresh: () => Promise<void>;
}

const AlertOverviewContext = createContext<AlertOverviewContextValue | null>(null);

export function AlertOverviewProvider({ children }: { children: React.ReactNode }) {
    const { user } = useAuth();
    const [overview, setOverview] = useState<AlertOverview | null>(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
    const controller = useRef<AbortController | null>(null);

    const refresh = useCallback(async () => {
        if (!user) return;
        controller.current?.abort();
        const next = new AbortController();
        controller.current = next;
        setLoading(true);
        try {
            const result = await alertOverviewApi.overview({ signal: next.signal });
            if (next.signal.aborted) return;
            setOverview(result);
            setUpdatedAt(new Date());
            setError(null);
        } catch (loadError) {
            if (next.signal.aborted) return;
            setError(loadError instanceof Error ? loadError.message : 'Failed to load alerts');
        } finally {
            if (!next.signal.aborted) setLoading(false);
        }
    }, [user]);

    useEffect(() => {
        if (!user) {
            controller.current?.abort();
            setOverview(null);
            setUpdatedAt(null);
            setError(null);
            setLoading(false);
            return;
        }
        void refresh();
        const timer = window.setInterval(() => void refresh(), REFRESH_INTERVAL);
        return () => {
            controller.current?.abort();
            window.clearInterval(timer);
        };
    }, [refresh, user]);

    const value = useMemo(
        () => ({ overview, loading, error, updatedAt, refresh }),
        [overview, loading, error, updatedAt, refresh]
    );
    return <AlertOverviewContext.Provider value={value}>{children}</AlertOverviewContext.Provider>;
}

export function useAlertOverview() {
    const context = useContext(AlertOverviewContext);
    if (!context) throw new Error('useAlertOverview must be used within AlertOverviewProvider');
    return context;
}
