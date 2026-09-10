import type { LucideIcon } from 'lucide-react';

import {
    BarChart3,
    BellRing,
    Cloud,
    FileClock,
    Flame,
    Globe,
    KeyRound,
    LayoutDashboard,
    ListTodo,
    Server,
    Settings,
    ShieldCheck,
    ShieldCog,
    Siren,
    Users,
    Waves,
    Waypoints,
} from 'lucide-react';
import { useLocation, useNavigate } from 'react-router-dom';

import AnimalStepIcon from './icons/AnimalStep';
import { NavItem } from '@/components/NavItem.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { useAuth } from '@/hooks/useAuth.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { useUnsavedChanges } from '@/hooks/useUnsavedChanges.tsx';
import { canManageCluster } from '@/utils/rbac.ts';

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
    { path: '/alerts', label: 'Alerts', icon: Siren },
    { path: '/analytics', label: 'Analytics', icon: BarChart3 },
];

function settingsNav(isPlatformAdmin: boolean, canManageAPIKeys: boolean): NavItemConfig {
    const children: NavItemConfig[] = [
        { path: '/settings', label: 'Security', icon: ShieldCheck },
        { path: '/settings/members', label: 'Cluster members', icon: Users },
        { path: '/settings/notifications', label: 'Notifications', icon: BellRing },
        { path: '/settings/logpush', label: 'Logpush', icon: Waves },
    ];
    if (canManageAPIKeys) {
        children.push({ path: '/settings/api-keys', label: 'API keys', icon: KeyRound });
    }
    if (isPlatformAdmin) {
        children.push({ path: '/settings/admin', label: 'Admin settings', icon: ShieldCog });
    }
    return {
        path: '/settings',
        label: 'Settings',
        icon: Settings,
        children,
    };
}

export function navigationFor(isPlatformAdmin = false, canManageAPIKeys = false) {
    return [...nav, settingsNav(isPlatformAdmin, canManageAPIKeys)];
}

interface SidebarProps {
    collapsed?: boolean;
    onNavigate?: () => void;
}

function SidebarBrand({ collapsed }: { collapsed?: boolean }) {
    if (collapsed) {
        return (
            <div className='flex justify-center px-3 pb-1 pt-4'>
                <div className='flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-foreground'>
                    <span className='text-xs font-bold'>G</span>
                </div>
            </div>
        );
    }

    return (
        <div className='flex items-center gap-2.5 px-4 pb-1 pt-4'>
            <AnimalStepIcon className='h-8 w-8' />
            <span className='truncate text-sm font-semibold tracking-tight' translate='no'>
                Goveto Edge
            </span>
        </div>
    );
}

function SidebarNav({ collapsed, onNavigate }: { collapsed?: boolean; onNavigate?: () => void }) {
    const location = useLocation();
    const { user } = useAuth();
    const { clusterId, clusters } = useCluster();
    const clusterRole = clusters.find((cluster) => cluster.id === clusterId)?.role;
    const visibleNav = navigationFor(user?.role === 'ADMIN', canManageCluster(clusterRole));

    return (
        <nav
            aria-label='Primary'
            className={`flex-1 space-y-1 overflow-y-auto p-3 pt-2 ${collapsed ? 'px-2' : ''}`}
        >
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

export function Sidebar({ collapsed, onNavigate }: SidebarProps) {
    return (
        <div className='flex h-full flex-col'>
            <SidebarBrand collapsed={collapsed} />
            <SidebarNav collapsed={collapsed} onNavigate={onNavigate} />
        </div>
    );
}

export function ClusterPicker() {
    const { clusterId, clusters, loading, setClusterId } = useCluster();
    const { requestAction } = useUnsavedChanges();
    const location = useLocation();
    const navigate = useNavigate();

    return (
        <SelectField
            ariaLabel='Current cluster'
            className='w-full'
            isDisabled={loading || clusters.length === 0}
            options={clusters.map((cluster) => ({ id: cluster.id, label: cluster.name }))}
            placeholder={loading ? 'Loading clusters…' : 'Select a cluster'}
            value={clusterId}
            onChange={(value) => {
                if (!value || value === clusterId) return;
                const target = clusters.find((cluster) => cluster.id === value);
                requestAction(
                    async () => {
                        await setClusterId(value);
                        if (
                            /^\/sites\/(?!create(?:\/|$)|logs(?:\/|$)|certificates(?:\/|$)|cache(?:\/|$))[^/]+/.test(
                                location.pathname
                            )
                        ) {
                            navigate('/sites');
                        } else if (
                            /^\/nodes\/(?!create(?:\/|$)|ssh-credentials(?:\/|$))[^/]+/.test(
                                location.pathname
                            )
                        ) {
                            navigate('/nodes');
                        }
                    },
                    `Unsaved changes will be discarded before switching to cluster "${target?.name ?? value}".`
                );
            }}
        />
    );
}
