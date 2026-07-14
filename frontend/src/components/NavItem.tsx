import type { LucideIcon } from 'lucide-react';

import { Badge } from '@heroui/react';
import { Link } from 'react-router-dom';

interface NavItemProps {
    to: string;
    icon: LucideIcon;
    label: string;
    active?: boolean;
    badge?: number;
    onClick?: () => void;
}

export function NavItem({ to, icon: Icon, label, active, badge, onClick }: NavItemProps) {
    return (
        <Link
            className={`group relative flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-colors ${
                active
                    ? 'bg-surface-secondary text-foreground before:absolute before:left-0 before:top-1/2 before:h-5 before:w-0.5 before:-translate-y-1/2 before:rounded-r-full before:bg-foreground'
                    : 'text-muted hover:bg-surface-secondary hover:text-foreground'
            }`}
            to={to}
            onClick={onClick}
        >
            <Icon className='h-[18px] w-[18px] shrink-0' />
            <span className='flex-1 truncate'>{label}</span>
            {badge ? (
                <Badge className='ml-auto' color='danger' size='sm' variant='soft'>
                    {badge}
                </Badge>
            ) : null}
        </Link>
    );
}
