import type { LucideIcon } from 'lucide-react';

import {
    Alert,
    Avatar,
    Button,
    Drawer,
    Dropdown,
    Input,
    useOverlayState,
    useTheme,
} from '@heroui/react';
import {
    BarChart3,
    ChevronDown,
    Globe,
    LayoutDashboard,
    LogOut,
    Menu,
    Moon,
    Rocket,
    Server,
    ShieldCheck,
    Sun,
    Trash2,
} from 'lucide-react';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';

import { NavItem } from '@/components/NavItem.tsx';
import { useAuth } from '@/hooks/useAuth.ts';
import { useCluster } from '@/hooks/useCluster.ts';

interface NavItemConfig {
    path: string;
    label: string;
    icon: LucideIcon;
}

const nav: NavItemConfig[] = [
    { path: '/', label: 'Dashboard', icon: LayoutDashboard },
    { path: '/nodes', label: 'Nodes', icon: Server },
    { path: '/sites', label: 'Sites', icon: Globe },
    { path: '/certificates', label: 'Certificates', icon: ShieldCheck },
    { path: '/publish', label: 'Publish Jobs', icon: Rocket },
    { path: '/purge', label: 'Purge Jobs', icon: Trash2 },
    { path: '/analytics', label: 'Analytics', icon: BarChart3 },
];

function SidebarContent({ onNavigate }: { onNavigate?: () => void }) {
    const location = useLocation();

    return (
        <div className='flex h-full flex-col'>
            <div className='flex h-16 items-center gap-2 px-4'>
                <div className='flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-foreground'>
                    <Globe className='h-5 w-5' />
                </div>
                <span className='text-lg font-bold'>Goveto Edge</span>
            </div>

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
        </div>
    );
}

export function Layout() {
    const navigate = useNavigate();
    const location = useLocation();
    const { user, logout } = useAuth();
    const { clusterId, setClusterId } = useCluster();
    const { resolvedTheme, setTheme } = useTheme();
    const mobileMenu = useOverlayState();

    const toggleTheme = () => {
        setTheme(resolvedTheme === 'dark' ? 'light' : 'dark');
    };

    const handleLogout = () => {
        logout();
        navigate('/login');
    };

    const userLabel = user?.name || user?.email || user?.id || 'User';
    const userInitial = userLabel.slice(0, 1).toUpperCase();

    return (
        <div className='flex h-full'>
            <aside className='hidden w-64 flex-col overflow-y-auto border-r border-border bg-surface md:flex'>
                <SidebarContent />
            </aside>

            <Drawer isOpen={mobileMenu.isOpen} onOpenChange={mobileMenu.setOpen}>
                <Drawer.Content className='w-[280px]'>
                    <SidebarContent onNavigate={mobileMenu.close} />
                </Drawer.Content>
            </Drawer>

            <div className='flex min-w-0 flex-1 flex-col'>
                <header className='flex h-16 shrink-0 items-center justify-between gap-4 border-b border-border bg-surface px-4'>
                    <div className='flex items-center gap-3'>
                        <Button
                            className='md:hidden'
                            isIconOnly
                            size='sm'
                            variant='ghost'
                            onPress={mobileMenu.open}
                        >
                            <Menu className='h-5 w-5' />
                        </Button>
                    </div>

                    <div className='flex flex-1 items-center justify-end gap-3'>
                        <div className='hidden items-center gap-2 md:flex'>
                            <span className='text-sm text-muted'>Cluster</span>
                            <Input
                                aria-label='Cluster ID'
                                className='w-64'
                                placeholder='cluster-id'
                                value={clusterId}
                                onChange={(e) => setClusterId(e.target.value)}
                            />
                        </div>

                        <div className='flex items-center gap-2'>
                            <Button isIconOnly size='sm' variant='ghost' onPress={toggleTheme}>
                                {resolvedTheme === 'dark' ? (
                                    <Sun className='h-4 w-4' />
                                ) : (
                                    <Moon className='h-4 w-4' />
                                )}
                            </Button>

                            <Dropdown>
                                <Dropdown.Trigger className='flex items-center gap-2 rounded-lg px-2 py-1 transition-colors hover:bg-surface-secondary'>
                                    <Avatar className='h-8 w-8 text-xs'>
                                        <Avatar.Fallback>{userInitial}</Avatar.Fallback>
                                    </Avatar>
                                    <span className='hidden max-w-[140px] truncate text-sm font-medium md:block'>
                                        {userLabel}
                                    </span>
                                    <ChevronDown className='hidden h-4 w-4 text-muted md:block' />
                                </Dropdown.Trigger>
                                <Dropdown.Popover className='min-w-[180px]'>
                                    <Dropdown.Menu aria-label='User menu'>
                                        <Dropdown.Item isDisabled className='text-muted'>
                                            Signed in as {userLabel}
                                        </Dropdown.Item>
                                        <Dropdown.Item onAction={handleLogout}>
                                            <span className='flex items-center gap-2'>
                                                <LogOut className='h-4 w-4' />
                                                Logout
                                            </span>
                                        </Dropdown.Item>
                                    </Dropdown.Menu>
                                </Dropdown.Popover>
                            </Dropdown>
                        </div>
                    </div>
                </header>

                <main className='flex-1 overflow-y-auto bg-background p-4 md:p-6'>
                    {!clusterId && location.pathname !== '/' && (
                        <Alert className='mb-6' status='warning'>
                            <Alert.Indicator />
                            <Alert.Content>
                                <Alert.Title>No cluster selected</Alert.Title>
                                <Alert.Description>
                                    Enter a cluster ID in the header to use cluster features.
                                </Alert.Description>
                            </Alert.Content>
                        </Alert>
                    )}
                    <Outlet />
                </main>
            </div>
        </div>
    );
}
