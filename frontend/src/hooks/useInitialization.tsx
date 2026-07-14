import { Button, Spinner } from '@heroui/react';
import {
    createContext,
    createElement,
    useCallback,
    useContext,
    useEffect,
    useMemo,
    useState,
} from 'react';
import { Navigate, useLocation } from 'react-router-dom';

import { initializationApi } from '@/api';

interface InitializationContextValue {
    initialized: boolean | null;
    complete: () => void;
    refresh: () => Promise<void>;
}

const InitializationContext = createContext<InitializationContextValue | null>(null);

export function InitializationProvider({ children }: { children: React.ReactNode }) {
    const [initialized, setInitialized] = useState<boolean | null>(null);
    const [error, setError] = useState('');

    const refresh = useCallback(async () => {
        setError('');
        try {
            const status = await initializationApi.status();
            setInitialized(status.initialized);
        } catch (err) {
            setError(err instanceof Error ? err.message : 'Unable to check instance status');
        }
    }, []);

    useEffect(() => {
        refresh();
    }, [refresh]);

    const value = useMemo(
        () => ({ initialized, complete: () => setInitialized(true), refresh }),
        [initialized, refresh]
    );

    if (error) {
        return (
            <div className='flex min-h-screen items-center justify-center p-6'>
                <div className='w-full max-w-md rounded-2xl border border-danger/20 bg-danger/10 p-6 text-center'>
                    <h1 className='text-lg font-semibold'>Unable to load instance status</h1>
                    <p className='mt-2 text-sm text-muted'>{error}</p>
                    <Button className='mt-5' variant='primary' onPress={refresh}>
                        Retry
                    </Button>
                </div>
            </div>
        );
    }

    return createElement(InitializationContext.Provider, { value }, children);
}

export function InitializationGate({ children }: { children: React.ReactNode }) {
    const { initialized } = useInitialization();
    const location = useLocation();

    if (initialized === null) {
        return (
            <div className='flex h-screen items-center justify-center'>
                <Spinner size='lg' />
            </div>
        );
    }
    if (!initialized && location.pathname !== '/init') {
        return <Navigate replace to='/init' />;
    }
    if (initialized && location.pathname === '/init') {
        return <Navigate replace to='/login' />;
    }
    return <>{children}</>;
}

export function useInitialization() {
    const context = useContext(InitializationContext);
    if (!context) {
        throw new Error('useInitialization must be used within an InitializationProvider');
    }
    return context;
}
