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
