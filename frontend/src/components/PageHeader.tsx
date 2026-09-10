import type { ReactNode } from 'react';

import { Server } from 'lucide-react';

import { useCluster } from '@/hooks/useCluster.ts';

interface Tab {
    id: string;
    label: string;
}

interface PageHeaderProps {
    title: string;
    subtitle?: string;
    /** Render a compact variant for pages embedded inside another page. */
    embedded?: boolean;
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
    embedded,
    tabs,
    activeTab,
    onTabChange,
    filters,
    actions,
    children,
}: PageHeaderProps) {
    const { clusterId, clusters } = useCluster();
    const clusterName = clusters.find((cluster) => cluster.id === clusterId)?.name;
    const rightActions = actions ?? children;
    return (
        <div className='space-y-4'>
            <div
                className={`flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between ${embedded ? '' : 'pt-2'}`}
            >
                <div>
                    {clusterName && !embedded && (
                        <div className='mb-1 flex items-center gap-1.5 text-xs font-medium text-muted'>
                            <Server aria-hidden='true' className='h-3.5 w-3.5' />
                            Cluster: {clusterName}
                        </div>
                    )}
                    {embedded ? (
                        <h2 className='text-lg font-semibold'>{title}</h2>
                    ) : (
                        <h1 className='text-2xl font-bold tracking-tight'>{title}</h1>
                    )}
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
                <div
                    aria-label={`${title} views`}
                    className='flex w-fit items-center gap-1 rounded-xl border border-border/60 bg-surface p-1'
                    role='tablist'
                    onKeyDown={(event) => {
                        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
                        event.preventDefault();
                        const tabEls = Array.from(
                            event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]')
                        );
                        const current = tabEls.indexOf(document.activeElement as HTMLButtonElement);
                        if (current === -1) return;
                        let next = current;
                        if (event.key === 'ArrowRight') next = (current + 1) % tabEls.length;
                        if (event.key === 'ArrowLeft')
                            next = (current - 1 + tabEls.length) % tabEls.length;
                        if (event.key === 'Home') next = 0;
                        if (event.key === 'End') next = tabEls.length - 1;
                        tabEls[next]?.focus();
                        tabEls[next]?.click();
                    }}
                >
                    {tabs.map((tab) => {
                        const active = activeTab === tab.id;
                        return (
                            <button
                                aria-selected={active}
                                key={tab.id}
                                className={`rounded-lg px-4 py-1.5 text-sm font-medium transition-colors ${
                                    active
                                        ? 'bg-accent-soft text-accent-soft-foreground'
                                        : 'text-muted hover:bg-surface-secondary hover:text-foreground'
                                }`}
                                onClick={() => onTabChange?.(tab.id)}
                                role='tab'
                                tabIndex={active ? 0 : -1}
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
