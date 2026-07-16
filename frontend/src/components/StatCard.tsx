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
    default: 'bg-surface text-foreground',
    primary: 'bg-primary/10 text-primary',
    success: 'bg-success/10 text-success',
    warning: 'bg-warning/10 text-warning',
    danger: 'bg-danger/10 text-danger',
};

export function StatCard({ icon: Icon, label, value, footer, color = 'default' }: StatCardProps) {
    return (
        <Card className='gap-0 rounded-xl border border-border/70 bg-surface p-4 shadow-sm'>
            <div className='flex items-start justify-between'>
                <div className='space-y-1.5'>
                    <p className='text-xs font-medium text-muted'>{label}</p>
                    <div className='text-2xl font-bold tracking-tight'>{value}</div>
                    {footer && <div className='text-xs text-muted'>{footer}</div>}
                </div>
                <div
                    className={`flex h-9 w-9 items-center justify-center rounded-lg ${colorStyles[color]}`}
                >
                    <Icon className='h-4.5 w-4.5' />
                </div>
            </div>
        </Card>
    );
}
