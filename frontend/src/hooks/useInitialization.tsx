import { Button, Spinner } from '@heroui/react';
import { RefreshCw } from 'lucide-react';
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
import { useSystemTheme } from '@/hooks/useSystemTheme.ts';

interface InitializationContextValue {
    initialized: boolean | null;
    complete: () => void;
    refresh: () => Promise<void>;
}

const InitializationContext = createContext<InitializationContextValue | null>(null);

function InitializationErrorPage({
    error,
    isRetrying,
    onRetry,
}: {
    error: string;
    isRetrying: boolean;
    onRetry: () => Promise<void>;
}) {
    useSystemTheme();

    return (
        <div className='min-h-[100dvh] bg-background px-5 py-6 text-foreground sm:px-8 sm:py-8'>
            <main className='mx-auto flex min-h-[calc(100dvh-3rem)] w-full max-w-xl flex-col sm:min-h-[calc(100dvh-4rem)]'>
                <section className='my-auto py-12'>
                    <h1 className='text-2xl font-semibold tracking-tight sm:text-[1.75rem]'>
                        Unable to load instance status
                    </h1>
                    <p className='mt-3 max-w-lg text-sm leading-6 text-muted'>
                        The control plane could not complete its startup check. Verify that the
                        service is running and reachable, then try again.
                    </p>

                    <div
                        className='mt-6 overflow-hidden rounded-xl border border-border/70 bg-surface shadow-sm'
                        role='alert'
                    >
                        <div className='px-4 py-3.5 sm:px-5'>
                            <div className='text-xs font-medium text-muted'>Error details</div>
                            <p className='mt-1.5 break-words font-mono text-xs leading-5 text-danger'>
                                {error}
                            </p>
                        </div>
                        <div className='flex flex-col gap-1 border-t border-border/70 bg-surface-secondary/35 px-4 py-3 text-xs sm:flex-row sm:items-center sm:justify-between sm:px-5'>
                            <span className='text-muted'>Endpoint</span>
                            <code className='break-all font-mono text-foreground'>
                                /api/v1/init/status
                            </code>
                        </div>
                    </div>

                    <div className='mt-6'>
                        <Button
                            className='w-full sm:w-auto'
                            isDisabled={isRetrying}
                            type='button'
                            variant='primary'
                            onPress={() => void onRetry()}
                        >
                            <RefreshCw
                                className={`h-4 w-4 ${isRetrying ? 'animate-spin motion-reduce:animate-none' : ''}`}
                            />
                            {isRetrying ? 'Checking status...' : 'Try again'}
                        </Button>
                    </div>
                </section>
            </main>
        </div>
    );
}

export function InitializationProvider({ children }: { children: React.ReactNode }) {
    const [initialized, setInitialized] = useState<boolean | null>(null);
    const [error, setError] = useState('');
    const [checking, setChecking] = useState(true);

    const refresh = useCallback(async () => {
        setChecking(true);
        try {
            const status = await initializationApi.status();
            setInitialized(status.initialized);
            setError('');
        } catch (err) {
            setError(err instanceof Error ? err.message : 'Unable to check instance status');
        } finally {
            setChecking(false);
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
        return <InitializationErrorPage error={error} isRetrying={checking} onRetry={refresh} />;
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
