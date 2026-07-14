import { createContext, createElement, useContext, useMemo, useSyncExternalStore } from 'react';

import { apiClient } from '@/api/client.ts';

interface ApiLoadingStore {
    pending: number;
    listeners: Set<() => void>;
}

const store: ApiLoadingStore = {
    pending: 0,
    listeners: new Set(),
};

function notify() {
    for (const listener of store.listeners) {
        listener();
    }
}

function subscribe(listener: () => void) {
    store.listeners.add(listener);
    return () => {
        store.listeners.delete(listener);
    };
}

function getSnapshot() {
    return store.pending;
}

let interceptorsInstalled = false;

function installInterceptors() {
    if (interceptorsInstalled) return;
    interceptorsInstalled = true;

    apiClient.interceptors.request.use((config) => {
        store.pending += 1;
        notify();
        return config;
    });

    apiClient.interceptors.response.use(
        (response) => {
            store.pending = Math.max(0, store.pending - 1);
            notify();
            return response;
        },
        (error) => {
            store.pending = Math.max(0, store.pending - 1);
            notify();
            return Promise.reject(error);
        }
    );
}

installInterceptors();

interface ApiLoadingContextValue {
    pending: number;
    isLoading: boolean;
}

const ApiLoadingContext = createContext<ApiLoadingContextValue | null>(null);

export function ApiLoadingProvider({ children }: { children: React.ReactNode }) {
    const pending = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
    const value = useMemo(() => ({ pending, isLoading: pending > 0 }), [pending]);
    return createElement(ApiLoadingContext.Provider, { value }, children);
}

export function useApiLoading(): ApiLoadingContextValue {
    const context = useContext(ApiLoadingContext);
    if (!context) {
        throw new Error('useApiLoading must be used within an ApiLoadingProvider');
    }
    return context;
}
