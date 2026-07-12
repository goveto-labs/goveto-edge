import type { ReactNode } from 'react';

interface PageHeaderProps {
    title: string;
    subtitle?: string;
    children?: ReactNode;
}

export function PageHeader({ title, subtitle, children }: PageHeaderProps) {
    return (
        <div className='flex flex-col gap-4 pb-6 pt-2 sm:flex-row sm:items-center sm:justify-between'>
            <div>
                <h1 className='text-2xl font-bold tracking-tight'>{title}</h1>
                {subtitle && <p className='mt-1 text-sm text-muted'>{subtitle}</p>}
            </div>
            {children && <div className='flex items-center gap-2'>{children}</div>}
        </div>
    );
}
