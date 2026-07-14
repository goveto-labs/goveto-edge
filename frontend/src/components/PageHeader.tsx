import type { ReactNode } from 'react';

interface Tab {
    id: string;
    label: string;
}

interface PageHeaderProps {
    title: string;
    subtitle?: string;
    tabs?: Tab[];
    activeTab?: string;
    onTabChange?: (id: string) => void;
    filters?: ReactNode;
    actions?: ReactNode;
    children?: ReactNode;
}

export function PageHeader({
    title,
    subtitle,
    tabs,
    activeTab,
    onTabChange,
    filters,
    actions,
    children,
}: PageHeaderProps) {
    const rightActions = actions ?? children;
    return (
        <div className='space-y-4'>
            <div className='flex flex-col gap-4 pt-2 sm:flex-row sm:items-start sm:justify-between'>
                <div>
                    <h1 className='text-2xl font-bold tracking-tight'>{title}</h1>
                    {subtitle && <p className='mt-1 text-sm text-muted'>{subtitle}</p>}
                </div>
                {(rightActions || filters) && (
                    <div className='flex flex-wrap items-center gap-2'>
                        {filters}
                        {rightActions}
                    </div>
                )}
            </div>

            {tabs && tabs.length > 0 && (
                <div className='flex items-center gap-1 rounded-xl bg-surface p-1 w-fit'>
                    {tabs.map((tab) => {
                        const active = activeTab === tab.id;
                        return (
                            <button
                                key={tab.id}
                                className={`rounded-lg px-4 py-1.5 text-sm font-medium transition-colors ${
                                    active
                                        ? 'bg-surface-secondary text-foreground shadow-sm'
                                        : 'text-muted hover:text-foreground'
                                }`}
                                onClick={() => onTabChange?.(tab.id)}
                                type='button'
                            >
                                {tab.label}
                            </button>
                        );
                    })}
                </div>
            )}
        </div>
    );
}
