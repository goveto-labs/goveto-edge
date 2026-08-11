import type { ReactNode } from 'react';

import { Alert, Button } from '@heroui/react';
import { AlertTriangle, Inbox, RefreshCw } from 'lucide-react';

export type AsyncStatus = 'loading' | 'empty' | 'error' | 'stale' | 'partial' | 'success';

interface AsyncStateProps {
    status: AsyncStatus;
    children?: ReactNode;
    error?: ReactNode;
    emptyTitle?: string;
    emptyDescription?: string;
    lastSuccessfulAt?: Date | null;
    onRetry?: () => void;
    retrying?: boolean;
}

export function AsyncState({
    status,
    children,
    error,
    emptyTitle = 'No data yet',
    emptyDescription = 'There is nothing to display for the current scope.',
    lastSuccessfulAt,
    onRetry,
    retrying,
}: AsyncStateProps) {
    if (status === 'loading') {
        return (
            <div aria-busy='true' aria-label='Loading' className='space-y-3' role='status'>
                {[0, 1, 2].map((row) => (
                    <div className='h-12 animate-pulse rounded-lg bg-surface-secondary' key={row} />
                ))}
            </div>
        );
    }
    if (status === 'empty') {
        return (
            <div className='px-4 py-10 text-center'>
                <Inbox aria-hidden='true' className='mx-auto h-7 w-7 text-muted' />
                <h2 className='mt-3 text-sm font-semibold'>{emptyTitle}</h2>
                <p className='mt-1 text-sm text-muted'>{emptyDescription}</p>
            </div>
        );
    }
    if (status === 'error' && !children) {
        return (
            <Alert status='danger'>
                <AlertTriangle className='h-4 w-4' />
                <Alert.Content>
                    <Alert.Title>Unable to load data</Alert.Title>
                    <Alert.Description>{error || 'The request failed.'}</Alert.Description>
                </Alert.Content>
                {onRetry && (
                    <Button isDisabled={retrying} size='sm' variant='secondary' onPress={onRetry}>
                        <RefreshCw className='h-4 w-4' />
                        Retry
                    </Button>
                )}
            </Alert>
        );
    }
    return (
        <div>
            {(status === 'stale' || status === 'partial' || status === 'error') && (
                <Alert className='mb-4' status={status === 'partial' ? 'warning' : 'danger'}>
                    <Alert.Content>
                        <Alert.Title>
                            {status === 'partial'
                                ? 'Some data is unavailable'
                                : 'Data may be stale'}
                        </Alert.Title>
                        <Alert.Description>
                            {error || 'Automatic refresh failed. Previously loaded data is shown.'}
                            {lastSuccessfulAt && (
                                <> Last updated {lastSuccessfulAt.toLocaleString()}.</>
                            )}
                        </Alert.Description>
                    </Alert.Content>
                    {onRetry && (
                        <Button
                            isDisabled={retrying}
                            size='sm'
                            variant='secondary'
                            onPress={onRetry}
                        >
                            <RefreshCw className='h-4 w-4' />
                            Retry
                        </Button>
                    )}
                </Alert>
            )}
            {children}
        </div>
    );
}
