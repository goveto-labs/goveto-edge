import type { LucideIcon } from 'lucide-react';

import { Badge, Tooltip } from '@heroui/react';
import { ChevronDown } from 'lucide-react';
import { Link, useNavigate } from 'react-router-dom';

import { useUnsavedChanges } from '@/hooks/useUnsavedChanges.tsx';
import { preloadRoute } from '@/routes.tsx';

interface NavItemProps {
    to: string;
    icon: LucideIcon;
    label: string;
    active?: boolean;
    badge?: number;
    collapsed?: boolean;
    onClick?: () => void;
    expanded?: boolean;
    onToggleExpand?: () => void;
}

export function NavItem({
    to,
    icon: Icon,
    label,
    active,
    badge,
    collapsed,
    onClick,
    expanded,
    onToggleExpand,
}: NavItemProps) {
    const navigate = useNavigate();
    const { requestAction } = useUnsavedChanges();
    const link = (
        <Link
            aria-current={active ? 'page' : undefined}
            className={`group relative flex items-center rounded-lg text-sm font-medium transition-colors cursor-pointer ${
                collapsed ? 'justify-center px-2 py-2.5' : 'gap-3 px-3 py-2.5'
            } ${
                active
                    ? 'bg-accent-soft text-accent-soft-foreground before:absolute before:left-0 before:top-1/2 before:h-5 before:w-0.5 before:-translate-y-1/2 before:rounded-r-full before:bg-accent'
                    : 'text-muted hover:bg-surface-secondary hover:text-foreground'
            }`}
            to={to}
            onClick={(event) => {
                if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey) {
                    onClick?.();
                    return;
                }
                event.preventDefault();
                requestAction(() => {
                    navigate(to);
                    onClick?.();
                });
            }}
            onFocus={() => preloadRoute(to)}
            onMouseEnter={() => preloadRoute(to)}
        >
            <Icon aria-hidden='true' className='h-[18px] w-[18px] shrink-0' />
            {!collapsed && <span className='flex-1 truncate'>{label}</span>}
            {!collapsed && badge ? (
                <Badge className='ml-auto tabular' color='danger' size='sm' variant='soft'>
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

    if (onToggleExpand) {
        return (
            <div className='flex items-center'>
                <div className='min-w-0 flex-1'>{link}</div>
                <button
                    aria-expanded={expanded}
                    aria-label={`${expanded ? 'Collapse' : 'Expand'} ${label}`}
                    className='flex h-8 w-8 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted transition-colors hover:bg-surface-secondary hover:text-foreground'
                    type='button'
                    onClick={onToggleExpand}
                >
                    <ChevronDown
                        aria-hidden='true'
                        className={`h-4 w-4 transition-transform duration-200 ${expanded ? '' : '-rotate-90'}`}
                    />
                </button>
            </div>
        );
    }

    return link;
}
