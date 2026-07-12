import { createContext, createElement, useContext, useEffect, useMemo, useState } from 'react';

const STORAGE_KEY = 'goveto-cluster-id';

interface ClusterContextValue {
    clusterId: string;
    setClusterId: (id: string) => void;
}

const ClusterContext = createContext<ClusterContextValue | null>(null);

export function ClusterProvider({ children }: { children: React.ReactNode }) {
    const [clusterId, setClusterId] = useState(() => {
        if (typeof window === 'undefined') return '';
        return localStorage.getItem(STORAGE_KEY) ?? '';
    });

    useEffect(() => {
        localStorage.setItem(STORAGE_KEY, clusterId);
    }, [clusterId]);

    const value = useMemo(
        () => ({
            clusterId,
            setClusterId,
        }),
        [clusterId]
    );

    return createElement(ClusterContext.Provider, { value }, children);
}

export function useCluster() {
    const context = useContext(ClusterContext);
    if (!context) {
        throw new Error('useCluster must be used within a ClusterProvider');
    }
    return context;
}
