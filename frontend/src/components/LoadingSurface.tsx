import type { ReactNode } from 'react';

interface LoadingSurfaceProps {
    children: ReactNode;
    isLoading: boolean;
    className?: string;
    label?: string;
}

export function LoadingSurface({
    children,
    isLoading,
    className = '',
    label = 'Loading',
}: LoadingSurfaceProps) {
    return (
        <div aria-busy={isLoading || undefined} className={`relative ${className}`}>
            <div
                aria-hidden={!isLoading}
                className={`absolute inset-0 z-40 overflow-hidden rounded-xl bg-background/55 backdrop-blur-[1px] transition-opacity duration-200 ease-out ${
                    isLoading ? 'opacity-100' : 'pointer-events-none opacity-0'
                }`}
                role='status'
            >
                <span className='sr-only'>{label}</span>
                <div className='absolute inset-x-0 top-0 h-1 overflow-hidden bg-primary/15'>
                    <div className='local-loading-progress h-full bg-primary shadow-[0_0_10px_var(--color-primary)]' />
                </div>
            </div>
            {children}
        </div>
    );
}
