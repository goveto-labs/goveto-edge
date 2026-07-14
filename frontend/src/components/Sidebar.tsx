import type { LucideIcon } from 'lucide-react';

import { Avatar, Button, ListBox, Select } from '@heroui/react';
import {
    BarChart3,
    Cloud,
    Globe,
    HelpCircle,
    LayoutDashboard,
    LogOut,
    Rocket,
    Server,
    ShieldCheck,
    Trash2,
} from 'lucide-react';
import { useLocation } from 'react-router-dom';

import { NavItem } from '@/components/NavItem.tsx';
import { useAuth } from '@/hooks/useAuth.ts';
import { useCluster } from '@/hooks/useCluster.ts';

interface NavItemConfig {
    path: string;
    label: string;
    icon: LucideIcon;
}

export const nav: NavItemConfig[] = [
    { path: '/', label: 'Dashboard', icon: LayoutDashboard },
    { path: '/nodes', label: 'Nodes', icon: Server },
    { path: '/sites', label: 'Sites', icon: Globe },
    { path: '/dns', label: 'DNS', icon: Cloud },
    { path: '/certificates', label: 'Certificates', icon: ShieldCheck },
    { path: '/publish', label: 'Publish Jobs', icon: Rocket },
    { path: '/purge', label: 'Purge Jobs', icon: Trash2 },
    { path: '/analytics', label: 'Analytics', icon: BarChart3 },
];

interface SidebarProps {
    onNavigate?: () => void;
    onLogout: () => void;
}

function SidebarProfile() {
    const { user } = useAuth();
    const label = user?.name || user?.email || user?.id || 'User';
    const role = user?.role || 'Admin';
    const initial = label.slice(0, 1).toUpperCase();

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

function SidebarNav({ onNavigate }: { onNavigate?: () => void }) {
    const location = useLocation();

    return (
        <nav className='flex-1 space-y-1 overflow-y-auto p-3 pt-0'>
            {nav.map((item) => (
                <NavItem
                    key={item.path}
                    active={location.pathname === item.path}
                    icon={item.icon}
                    label={item.label}
                    onClick={onNavigate}
                    to={item.path}
                />
            ))}
        </nav>
    );
}

function SidebarFooter({ onLogout }: { onLogout: () => void }) {
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

export function Sidebar({ onNavigate, onLogout }: SidebarProps) {
    return (
        <div className='flex h-full flex-col'>
            <SidebarProfile />
            <SidebarNav onNavigate={onNavigate} />
            <SidebarFooter onLogout={onLogout} />
        </div>
    );
}

export function ClusterPicker() {
    const { clusterId, clusters, loading, setClusterId } = useCluster();

    return (
        <Select
            aria-label='Current cluster'
            className='w-full'
            value={clusterId}
            onChange={(key) => {
                if (key) void setClusterId(String(key));
            }}
        >
            <Select.Trigger>
                <Select.Value>{loading ? 'Loading clusters…' : undefined}</Select.Value>
            </Select.Trigger>
            <Select.Popover>
                <ListBox>
                    {clusters.map((cluster) => (
                        <ListBox.Item id={cluster.id} key={cluster.id} textValue={cluster.name}>
                            {cluster.name}
                        </ListBox.Item>
                    ))}
                </ListBox>
            </Select.Popover>
        </Select>
    );
}
