import type { LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';

import { Card } from '@heroui/react';

interface StatCardProps {
    icon: LucideIcon;
    label: string;
    value: ReactNode;
    footer?: ReactNode;
    color?: 'default' | 'primary' | 'success' | 'warning' | 'danger';
}

const colorStyles = {
    default: 'bg-surface-secondary text-muted',
    primary: 'bg-accent-soft text-accent-soft-foreground',
    success: 'bg-success-soft text-success-soft-foreground',
    warning: 'bg-warning-soft text-warning-soft-foreground',
    danger: 'bg-danger-soft text-danger-soft-foreground',
};

export function StatCard({ icon: Icon, label, value, footer, color = 'default' }: StatCardProps) {
    return (
        <Card className='gap-0 rounded-xl border border-border/70 bg-surface p-4 shadow-sm'>
            <div className='flex items-start justify-between'>
                <div className='min-w-0 space-y-1.5'>
                    <p className='truncate text-xs font-medium text-muted'>{label}</p>
                    <div className='tabular text-2xl font-bold tracking-tight'>{value}</div>
                    {footer && <div className='truncate text-xs text-muted'>{footer}</div>}
                </div>
                <div
                    className={`flex h-9 w-9 shrink-0 items-center justify-center rounded-lg ${colorStyles[color]}`}
                >
                    <Icon aria-hidden='true' className='h-4.5 w-4.5' />
                </div>
            </div>
        </Card>
    );
}
