import type { ReactNode } from 'react';

import { Card } from '@heroui/react';

interface DataTableProps {
    children: ReactNode;
    title?: ReactNode;
    action?: ReactNode;
    className?: string;
}

export function DataTable({ children, title, action, className = '' }: DataTableProps) {
    return (
        <Card className={`overflow-hidden ${className}`}>
            {(title || action) && (
                <div className='flex flex-col gap-3 border-b border-border p-4 sm:flex-row sm:items-center sm:justify-between'>
                    {title && <div className='text-sm font-semibold'>{title}</div>}
                    {action && <div className='flex items-center gap-2'>{action}</div>}
                </div>
            )}
            <div className='overflow-x-auto'>{children}</div>
        </Card>
    );
}
