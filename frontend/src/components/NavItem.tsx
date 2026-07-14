import type { LucideIcon } from 'lucide-react';

import { Badge, Tooltip } from '@heroui/react';
import { Link } from 'react-router-dom';

interface NavItemProps {
    to: string;
    icon: LucideIcon;
    label: string;
    active?: boolean;
    badge?: number;
    collapsed?: boolean;
    onClick?: () => void;
}

export function NavItem({
    to,
    icon: Icon,
    label,
    active,
    badge,
    collapsed,
    onClick,
}: NavItemProps) {
    const link = (
        <Link
            className={`group relative flex items-center rounded-lg text-sm font-medium transition-colors cursor-pointer ${
                collapsed ? 'justify-center px-2 py-2.5' : 'gap-3 px-3 py-2.5'
            } ${
                active
                    ? 'bg-surface-secondary text-foreground before:absolute before:left-0 before:top-1/2 before:h-5 before:w-0.5 before:-translate-y-1/2 before:rounded-r-full before:bg-foreground'
                    : 'text-muted hover:bg-surface-secondary hover:text-foreground'
            }`}
            to={to}
            onClick={onClick}
        >
            <Icon className='h-[18px] w-[18px] shrink-0' />
            {!collapsed && <span className='flex-1 truncate'>{label}</span>}
            {!collapsed && badge ? (
                <Badge className='ml-auto' color='danger' size='sm' variant='soft'>
                    {badge}
                </Badge>
            ) : null}
            {collapsed && badge ? (
                <span className='absolute right-1 top-1.5 h-2 w-2 rounded-full bg-danger' />
            ) : null}
        </Link>
    );

    if (collapsed) {
        return (
            <Tooltip>
                <Tooltip.Trigger>{link}</Tooltip.Trigger>
                <Tooltip.Content>{label}</Tooltip.Content>
            </Tooltip>
        );
    }

    return link;
}
