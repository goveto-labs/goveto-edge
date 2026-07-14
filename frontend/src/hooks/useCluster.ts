import type { ClusterChoice } from '@/api/types.ts';

import { createContext, createElement, useCallback, useContext, useEffect, useMemo, useState } from 'react';

import { clustersApi } from '@/api';
import { useAuth } from '@/hooks/useAuth.ts';

interface ClusterContextValue {
    clusterId: string;
    clusters: ClusterChoice[];
    loading: boolean;
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

    const refresh = useCallback(async () => {
        if (!user) { setClusters([]); setCurrentClusterId(''); return; }
        setLoading(true);
        try {
            const result = await clustersApi.list();
            setClusters(result.clusters ?? []);
            setCurrentClusterId(result.selected_cluster_id ?? '');
            setError(null);
        } catch (err) {
            setError(err instanceof Error ? err.message : 'Unable to load clusters');
        } finally { setLoading(false); }
    }, [user]);

    useEffect(() => { void refresh(); }, [refresh]);

    const setClusterId = useCallback(async (id: string) => {
        await clustersApi.select(id);
        setCurrentClusterId(id);
    }, []);

    const createCluster = useCallback(async (name: string) => {
        const result = await clustersApi.create(name);
        await refresh();
        setCurrentClusterId(result.selected_cluster_id);
    }, [refresh]);

    const value = useMemo(() => ({ clusterId, clusters, loading, error, requiresCluster: !loading && !!user && clusters.length === 0, setClusterId, createCluster, refresh }), [clusterId, clusters, loading, error, user, setClusterId, createCluster, refresh]);
    return createElement(ClusterContext.Provider, { value }, children);
}

export function useCluster() {
    const context = useContext(ClusterContext);
    if (!context) throw new Error('useCluster must be used within a ClusterProvider');
    return context;
}
