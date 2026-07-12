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
        <Card className='p-5'>
            <div className='flex items-start justify-between'>
                <div className='space-y-3'>
                    <p className='text-sm font-medium text-muted'>{label}</p>
                    <div className='text-3xl font-bold tracking-tight'>{value}</div>
                    {footer && <div className='text-xs text-muted'>{footer}</div>}
                </div>
                <div
                    className={`flex h-10 w-10 items-center justify-center rounded-xl ${colorStyles[color]}`}
                >
                    <Icon className='h-5 w-5' />
                </div>
            </div>
        </Card>
    );
}
