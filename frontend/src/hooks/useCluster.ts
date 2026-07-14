import type { ClusterChoice } from '@/api/types.ts';

import {
    createContext,
    createElement,
    useCallback,
    useContext,
    useEffect,
    useMemo,
    useRef,
    useState,
} from 'react';

import { clustersApi } from '@/api';
import { useAuth } from '@/hooks/useAuth.ts';

interface ClusterContextValue {
    clusterId: string;
    clusters: ClusterChoice[];
    loading: boolean;
    ready: boolean;
    error: string | null;
    requiresCluster: boolean;
    setClusterId: (id: string) => Promise<void>;
    createCluster: (name: string) => Promise<void>;
    refresh: () => Promise<void>;
}

const ClusterContext = createContext<ClusterContextValue | null>(null);

export function ClusterProvider({ children }: { children: React.ReactNode }) {
    const { user } = useAuth();
    const [clusterId, setCurrentClusterId] = useState('');
    const [clusters, setClusters] = useState<ClusterChoice[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [loadedForUserId, setLoadedForUserId] = useState<string | null>(null);
    const requestVersion = useRef(0);
    const ready = !!user && loadedForUserId === user.id;

    const refresh = useCallback(async () => {
        const userId = user?.id ?? null;
        const version = ++requestVersion.current;

        if (!userId) {
            setClusters([]);
            setCurrentClusterId('');
            setLoadedForUserId(null);
            setLoading(false);
            setError(null);
            return;
        }

        setLoading(true);
        try {
            const result = await clustersApi.list();
            if (version !== requestVersion.current) return;
            setClusters(result.clusters ?? []);
            setCurrentClusterId(result.selected_cluster_id ?? '');
            setLoadedForUserId(userId);
            setError(null);
        } catch (err) {
            if (version !== requestVersion.current) return;
            setLoadedForUserId(null);
            setError(err instanceof Error ? err.message : 'Unable to load clusters');
        } finally {
            if (version === requestVersion.current) setLoading(false);
        }
    }, [user?.id]);

    useEffect(() => {
        void refresh();
        return () => {
            requestVersion.current++;
        };
    }, [refresh]);

    const setClusterId = useCallback(async (id: string) => {
        await clustersApi.select(id);
        setCurrentClusterId(id);
    }, []);

    const createCluster = useCallback(
        async (name: string) => {
            const result = await clustersApi.create(name);
            await refresh();
            setCurrentClusterId(result.selected_cluster_id);
        },
        [refresh]
    );

    const value = useMemo(
        () => ({
            clusterId,
            clusters,
            loading,
            ready,
            error,
            requiresCluster: ready && !loading && clusters.length === 0,
            setClusterId,
            createCluster,
            refresh,
        }),
        [clusterId, clusters, loading, ready, error, setClusterId, createCluster, refresh]
    );
    return createElement(ClusterContext.Provider, { value }, children);
}

export function useCluster() {
    const context = useContext(ClusterContext);
    if (!context) throw new Error('useCluster must be used within a ClusterProvider');
    return context;
}
