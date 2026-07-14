import { Loader2 } from 'lucide-react';

import { useApiLoading } from '@/hooks/useApiLoading.tsx';

export function ApiSpinner() {
    const { isLoading } = useApiLoading();
    if (!isLoading) return null;

    return (
        <div
            aria-busy='true'
            aria-label='Loading'
            className='pointer-events-none absolute left-0 top-0 z-20 text-foreground'
            role='status'
        >
            <Loader2 className='h-8 w-8 animate-spin text-primary' />
        </div>
    );
}
