import type { ErrorInfo, ReactNode } from 'react';

import { Button } from '@heroui/react';
import { AlertTriangle, Home, RefreshCw } from 'lucide-react';
import { Component } from 'react';

import { ApiError } from '@/api';

interface Props {
    children: ReactNode;
}

interface State {
    error: Error | null;
    errorReference: string;
}

function isChunkLoadError(error: Error) {
    return /chunk|dynamically imported module|loading css chunk|importing a module script/i.test(
        error.message
    );
}

export class RouteErrorBoundary extends Component<Props, State> {
    state: State = { error: null, errorReference: '' };

    static getDerivedStateFromError(error: Error): State {
        const requestID = error instanceof ApiError ? error.requestId : undefined;
        return {
            error,
            errorReference: requestID || `UI-${crypto.randomUUID().slice(0, 8).toUpperCase()}`,
        };
    }

    componentDidCatch(error: Error, info: ErrorInfo) {
        console.error('Route render failed', error, info.componentStack);
    }

    private retry = () => {
        if (this.state.error && isChunkLoadError(this.state.error)) {
            window.location.reload();
            return;
        }
        this.setState({ error: null, errorReference: '' });
    };

    render() {
        if (!this.state.error) return this.props.children;

        return (
            <main className='flex min-h-[100dvh] items-center justify-center bg-background p-6'>
                <section
                    aria-labelledby='route-error-title'
                    className='w-full max-w-xl rounded-xl border border-border bg-surface p-6 shadow-sm'
                    role='alert'
                >
                    <AlertTriangle aria-hidden='true' className='mb-4 h-8 w-8 text-danger' />
                    <h1 className='text-xl font-semibold' id='route-error-title'>
                        This page could not be displayed
                    </h1>
                    <p className='mt-2 text-sm leading-6 text-muted'>
                        Your current page is unavailable. Retry the request or return to the
                        dashboard to continue safely.
                    </p>
                    <dl className='mt-4 rounded-lg bg-surface-secondary px-3 py-2 text-sm'>
                        <div className='flex flex-wrap justify-between gap-2'>
                            <dt className='text-muted'>Request ID</dt>
                            <dd className='font-mono'>{this.state.errorReference}</dd>
                        </div>
                    </dl>
                    <div className='mt-6 flex flex-wrap gap-2'>
                        <Button onPress={this.retry}>
                            <RefreshCw className='h-4 w-4' />
                            Retry
                        </Button>
                        <Button variant='secondary' onPress={() => window.location.assign('/')}>
                            <Home className='h-4 w-4' />
                            Return to dashboard
                        </Button>
                    </div>
                </section>
            </main>
        );
    }
}
