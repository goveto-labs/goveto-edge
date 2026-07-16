import type { ReactNode } from 'react';

import { Card } from '@heroui/react';

interface ContentCardProps {
    children: ReactNode;
    title?: ReactNode;
    action?: ReactNode;
    className?: string;
    noPadding?: boolean;
    allowOverflow?: boolean;
}

export function ContentCard({
    children,
    title,
    action,
    noPadding = false,
    allowOverflow = false,
    className = '',
}: ContentCardProps) {
    return (
        <Card
            className={`gap-0 rounded-xl border border-border/70 bg-surface p-0 shadow-sm ${allowOverflow ? 'relative overflow-visible' : 'overflow-hidden'} ${className}`}
        >
            {(title || action) && (
                <div className='flex flex-col gap-2 border-b border-border px-4 py-3 sm:flex-row sm:items-center sm:justify-between'>
                    {title && <div className='text-sm font-semibold'>{title}</div>}
                    {action && <div className='flex items-center gap-2'>{action}</div>}
                </div>
            )}
            <div
                className={
                    noPadding
                        ? allowOverflow
                            ? 'overflow-visible'
                            : 'overflow-hidden'
                        : allowOverflow
                          ? 'overflow-visible p-3.5'
                          : 'p-3.5'
                }
            >
                {children}
            </div>
        </Card>
    );
}
