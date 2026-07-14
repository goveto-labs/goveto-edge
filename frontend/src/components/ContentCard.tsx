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
        <Card className={`overflow-hidden ${className}`}>
            {(title || action) && (
                <div className='flex flex-col gap-3 border-b border-border p-5 sm:flex-row sm:items-center sm:justify-between'>
                    {title && <div className='text-sm font-semibold'>{title}</div>}
                    {action && <div className='flex items-center gap-2'>{action}</div>}
                </div>
            )}
            <div className={noPadding ? 'overflow-hidden rounded-3xl' : 'p-5'}>{children}</div>
        </Card>
    );
}
