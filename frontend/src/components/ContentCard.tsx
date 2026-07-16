import type { ReactNode } from 'react';

import { Card } from '@heroui/react';

interface ContentCardProps {
    children: ReactNode;
    title?: ReactNode;
    action?: ReactNode;
    className?: string;
    noPadding?: boolean;
}

export function ContentCard({
    children,
    title,
    action,
    noPadding = false,
    className = '',
}: ContentCardProps) {
    return (
        <Card
            className={`gap-0 overflow-hidden rounded-xl border border-border/70 bg-surface p-0 shadow-sm ${className}`}
        >
            {(title || action) && (
                <div className='flex flex-col gap-2 border-b border-border px-4 py-3 sm:flex-row sm:items-center sm:justify-between'>
                    {title && <div className='text-sm font-semibold'>{title}</div>}
                    {action && <div className='flex items-center gap-2'>{action}</div>}
                </div>
            )}
            <div className={noPadding ? 'overflow-hidden' : 'p-3.5'}>{children}</div>
        </Card>
    );
}
