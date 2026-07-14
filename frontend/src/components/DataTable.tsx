import type { ReactNode } from 'react';

import { Card, Spinner } from '@heroui/react';
import { Inbox } from 'lucide-react';

interface DataTableProps {
    children?: ReactNode;
    title?: ReactNode;
    action?: ReactNode;
    className?: string;
    empty?: boolean;
    emptyTitle?: string;
    emptyDescription?: string;
    emptyAction?: ReactNode;
    loading?: boolean;
    'aria-label'?: string;
}

export function DataTable({
    children,
    title,
    action,
    className = '',
    empty = false,
    emptyTitle = 'No data yet',
    emptyDescription = 'There is nothing to display right now.',
    emptyAction,
    loading = false,
    'aria-label': ariaLabel,
}: DataTableProps) {
    return (
        <Card
            className={`overflow-hidden border border-border/70 bg-surface shadow-sm ${className}`}
        >
            {(title || action) && (
                <div className='flex min-h-14 flex-col gap-3 border-b border-border bg-surface px-5 py-3 sm:flex-row sm:items-center sm:justify-between'>
                    {title && <div className='text-sm font-semibold tracking-tight'>{title}</div>}
                    {action && <div className='flex items-center gap-2'>{action}</div>}
                </div>
            )}
            {loading ? (
                <div className='flex min-h-52 items-center justify-center' role='status'>
                    <Spinner />
                </div>
            ) : empty ? (
                <div
                    className='flex min-h-52 flex-col items-center justify-center px-6 py-10 text-center'
                    role='status'
                >
                    <div className='mb-4 flex h-12 w-12 items-center justify-center rounded-2xl border border-border bg-surface-secondary text-muted'>
                        <Inbox className='h-5 w-5' />
                    </div>
                    <h3 className='text-sm font-semibold'>{emptyTitle}</h3>
                    <p className='mt-1 max-w-sm text-sm leading-6 text-muted'>{emptyDescription}</p>
                    {emptyAction && <div className='mt-5'>{emptyAction}</div>}
                </div>
            ) : (
                <div className='overflow-x-auto'>
                    <table
                        aria-label={ariaLabel}
                        className='w-full min-w-max border-collapse text-left [&_tbody_tr]:border-b [&_tbody_tr]:border-border/70 [&_tbody_tr]:transition-colors [&_tbody_tr:last-child]:border-0 [&_tbody_tr:hover]:bg-surface-secondary/35 [&_td]:px-5 [&_td]:py-3.5 [&_th]:h-11 [&_th]:bg-surface-secondary/45 [&_th]:px-5 [&_th]:py-0 [&_th]:text-xs [&_th]:font-medium [&_th]:uppercase [&_th]:tracking-wide [&_th]:text-muted'
                    >
                        {children}
                    </table>
                </div>
            )}
        </Card>
    );
}
