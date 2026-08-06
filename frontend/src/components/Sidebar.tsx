import type { LucideIcon } from 'lucide-react';

import { Avatar, Button, Tooltip } from '@heroui/react';
import {
    BarChart3,
    Cloud,
    FileClock,
    Flame,
    Globe,
    HelpCircle,
    KeyRound,
    LayoutDashboard,
    ListTodo,
    LogOut,
    Server,
    Settings,
    ShieldCheck,
    ShieldCog,
    Waypoints,
} from 'lucide-react';
import { useLocation } from 'react-router-dom';

import { NavItem } from '@/components/NavItem.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { useAuth } from '@/hooks/useAuth.ts';
import { useCluster } from '@/hooks/useCluster.ts';

interface NavItemConfig {
    path: string;
    label: string;
    icon: LucideIcon;
    children?: NavItemConfig[];
}

const nav: NavItemConfig[] = [
    { path: '/', label: 'Overview', icon: LayoutDashboard },
    {
        path: '/nodes',
        label: 'Nodes',
        icon: Server,
        children: [{ path: '/nodes/ssh-credentials', label: 'SSH credentials', icon: KeyRound }],
    },

    {
        path: '/sites',
        label: 'Sites',
        icon: Globe,
        children: [
            { path: '/sites/logs', label: 'Access logs', icon: FileClock },
            { path: '/sites/certificates', label: 'Certificates', icon: ShieldCheck },
            { path: '/sites/cache', label: 'Cache operations', icon: Flame },
        ],
    },
    {
        path: '/dns',
        label: 'DNS',
        icon: Cloud,
        children: [{ path: '/dns/zones', label: 'DNS zones', icon: Waypoints }],
    },
    { path: '/jobs', label: 'Jobs', icon: ListTodo },
    { path: '/analytics', label: 'Analytics', icon: BarChart3 },
];

function settingsNav(isInstanceOwner: boolean): NavItemConfig {
    const children: NavItemConfig[] = [{ path: '/settings', label: 'Security', icon: ShieldCheck }];
    if (isInstanceOwner) {
        children.push({ path: '/settings/admin', label: 'Admin settings', icon: ShieldCog });
    }
    return {
        path: '/settings',
        label: 'Settings',
        icon: Settings,
        children,
    };
}

export function navigationFor(isInstanceOwner: boolean) {
    return [...nav, settingsNav(isInstanceOwner)];
}

interface SidebarProps {
    collapsed?: boolean;
    onNavigate?: () => void;
    onLogout: () => void;
}

function SidebarProfile({ collapsed }: { collapsed?: boolean }) {
    const { user } = useAuth();
    const label = user?.name || user?.email || user?.id || 'User';
    const role = user?.role || 'Admin';
    const initial = label.slice(0, 1).toUpperCase();

    if (collapsed) {
        return (
            <div className='flex justify-center px-3 py-4'>
                <Tooltip>
                    <Tooltip.Trigger>
                        <Avatar className='h-10 w-10 text-xs'>
                            <Avatar.Fallback>{initial}</Avatar.Fallback>
                        </Avatar>
                    </Tooltip.Trigger>
                    <Tooltip.Content>{`${label} · ${role}`}</Tooltip.Content>
                </Tooltip>
            </div>
        );
    }

    return (
        <div className='flex items-center gap-3 px-3 py-4'>
            <Avatar className='h-10 w-10 text-xs'>
                <Avatar.Fallback>{initial}</Avatar.Fallback>
            </Avatar>
            <div className='min-w-0'>
                <div className='truncate text-sm font-semibold'>{label}</div>
                <div className='truncate text-xs text-muted'>{role}</div>
            </div>
        </div>
    );
}

function SidebarNav({ collapsed, onNavigate }: { collapsed?: boolean; onNavigate?: () => void }) {
    const location = useLocation();
    const { user } = useAuth();
    const visibleNav = navigationFor(Boolean(user?.is_instance_owner));

    return (
        <nav className={`flex-1 space-y-1 overflow-y-auto p-3 pt-0 ${collapsed ? 'px-2' : ''}`}>
            {visibleNav.map((item) => {
                const activeChild =
                    item.children?.find((child) => location.pathname === child.path) ??
                    item.children
                        ?.filter((child) => child.path !== item.path)
                        .sort((left, right) => right.path.length - left.path.length)
                        .find((child) => location.pathname.startsWith(`${child.path}/`));
                const active =
                    location.pathname === item.path ||
                    (item.path !== '/' &&
                        !activeChild &&
                        location.pathname.startsWith(`${item.path}/`));
                const expanded = active || Boolean(activeChild);
                return (
                    <div key={item.path}>
                        <NavItem
                            active={active}
                            collapsed={collapsed}
                            icon={item.icon}
                            label={item.label}
                            onClick={onNavigate}
                            to={item.path}
                        />
                        {!collapsed && expanded && item.children && (
                            <div className='ml-5 mt-1 space-y-1 border-l border-border pl-2'>
                                {item.children.map((child) => (
                                    <NavItem
                                        key={child.path}
                                        active={activeChild?.path === child.path}
                                        icon={child.icon}
                                        label={child.label}
                                        onClick={onNavigate}
                                        to={child.path}
                                    />
                                ))}
                            </div>
                        )}
                    </div>
                );
            })}
        </nav>
    );
}

function SidebarFooter({ collapsed, onLogout }: { collapsed?: boolean; onLogout: () => void }) {
    if (collapsed) {
        return (
            <div className='space-y-1 border-t border-border p-2'>
                <Tooltip>
                    <Tooltip.Trigger>
                        <Button
                            className='w-full justify-center px-2 text-muted'
                            isIconOnly
                            variant='ghost'
                        >
                            <HelpCircle className='h-[18px] w-[18px]' />
                        </Button>
                    </Tooltip.Trigger>
                    <Tooltip.Content>Help & Information</Tooltip.Content>
                </Tooltip>
                <Tooltip>
                    <Tooltip.Trigger>
                        <Button
                            className='w-full justify-center px-2 text-muted'
                            isIconOnly
                            variant='ghost'
                            onPress={onLogout}
                        >
                            <LogOut className='h-[18px] w-[18px]' />
                        </Button>
                    </Tooltip.Trigger>
                    <Tooltip.Content>Log out</Tooltip.Content>
                </Tooltip>
            </div>
        );
    }

    return (
        <div className='space-y-1 border-t border-border p-3'>
            <Button
                className='w-full justify-start gap-3 text-sm font-medium text-muted'
                variant='ghost'
            >
                <HelpCircle className='h-[18px] w-[18px]' />
                Help & Information
            </Button>
            <Button
                className='w-full justify-start gap-3 text-sm font-medium text-muted'
                variant='ghost'
                onPress={onLogout}
            >
                <LogOut className='h-[18px] w-[18px]' />
                Log out
            </Button>
        </div>
    );
}

export function Sidebar({ collapsed, onNavigate, onLogout }: SidebarProps) {
    return (
        <div className='flex h-full flex-col'>
            <SidebarProfile collapsed={collapsed} />
            <SidebarNav collapsed={collapsed} onNavigate={onNavigate} />
            <SidebarFooter collapsed={collapsed} onLogout={onLogout} />
        </div>
    );
}

export function ClusterPicker() {
    const { clusterId, clusters, loading, setClusterId } = useCluster();

    return (
        <SelectField
            ariaLabel='Current cluster'
            className='w-full'
            isDisabled={loading || clusters.length === 0}
            options={clusters.map((cluster) => ({ id: cluster.id, label: cluster.name }))}
            placeholder={loading ? 'Loading clusters…' : 'Select a cluster'}
            value={clusterId}
            onChange={(value) => {
                if (value) void setClusterId(value);
            }}
        />
    );
}
