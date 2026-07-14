import { Alert, Avatar, Button, Drawer, Input, useOverlayState, useTheme } from '@heroui/react';
import { Bell, ChevronLeft, ChevronRight, Menu, Moon, Plus, Sun } from 'lucide-react';
import { useMemo, useState } from 'react';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';

import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { ClusterPicker, nav, Sidebar } from '@/components/Sidebar.tsx';
import { useAuth } from '@/hooks/useAuth.ts';
import { useCluster } from '@/hooks/useCluster.ts';

function Greeting() {
    const { user } = useAuth();
    const hour = new Date().getHours();
    const greeting = hour < 12 ? 'Good morning' : hour < 18 ? 'Good afternoon' : 'Good evening';
    const name = user?.name || user?.email || user?.id || 'there';

    return (
        <div className='hidden items-center gap-2 lg:flex'>
            <span className='text-sm font-medium text-muted'>
                {greeting}, {name}
            </span>
        </div>
    );
}

function PageTitle({ pathname }: { pathname: string }) {
    const item = useMemo(() => nav.find((n) => n.path === pathname), [pathname]);
    const title = item?.label ?? 'Dashboard';

    return <span className='text-lg font-semibold'>{title}</span>;
}

export function Layout() {
    const navigate = useNavigate();
    const location = useLocation();
    const { user, logout } = useAuth();
    const {
        clusterId,
        error: clusterError,
        ready: clustersReady,
        requiresCluster,
        createCluster,
    } = useCluster();
    const { resolvedTheme, setTheme } = useTheme();
    const mobileMenu = useOverlayState();
    const createModal = useOverlayState();
    const [clusterName, setClusterName] = useState('');
    const [creating, setCreating] = useState(false);
    const [createError, setCreateError] = useState<string | null>(null);
    const [sidebarCollapsed, setSidebarCollapsed] = useState(false);

    const toggleTheme = () => {
        setTheme(resolvedTheme === 'dark' ? 'light' : 'dark');
    };

    const handleLogout = () => {
        logout();
        navigate('/login');
    };

    const handleCreateCluster = async (event: React.FormEvent) => {
        event.preventDefault();
        setCreating(true);
        setCreateError(null);
        try {
            await createCluster(clusterName);
            setClusterName('');
            createModal.close();
        } catch (err) {
            setCreateError(err instanceof Error ? err.message : 'Unable to create cluster');
        } finally {
            setCreating(false);
        }
    };

    const userLabel = user?.name || user?.email || user?.id || 'User';
    const userInitial = userLabel.slice(0, 1).toUpperCase();

    return (
        <div className='flex h-full'>
            <aside
                className={`hidden flex-col overflow-y-auto border-r border-border bg-surface transition-[width] duration-300 ease-in-out md:flex ${
                    sidebarCollapsed ? 'w-[72px]' : 'w-64'
                }`}
            >
                <Sidebar collapsed={sidebarCollapsed} onLogout={handleLogout} />
            </aside>

            <div className='flex min-w-0 flex-1 flex-col'>
                <header className='sticky top-0 z-30 flex h-14 shrink-0 items-center justify-between gap-4 border-b border-border bg-surface/95 px-4 backdrop-blur supports-[backdrop-filter]:bg-surface/80'>
                    <div className='flex items-center gap-3'>
                        <Drawer state={mobileMenu}>
                            <Drawer.Trigger aria-label='Open navigation' className='md:hidden'>
                                <Menu className='h-5 w-5' />
                            </Drawer.Trigger>
                            <Drawer.Content className='w-[280px]'>
                                <Sidebar onLogout={handleLogout} onNavigate={mobileMenu.close} />
                            </Drawer.Content>
                        </Drawer>

                        <Button
                            aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
                            className='hidden md:flex'
                            isIconOnly
                            size='sm'
                            variant='ghost'
                            onPress={() => setSidebarCollapsed((c) => !c)}
                        >
                            {sidebarCollapsed ? (
                                <ChevronRight className='h-4 w-4' />
                            ) : (
                                <ChevronLeft className='h-4 w-4' />
                            )}
                        </Button>

                        <div className='flex items-center gap-2 md:hidden'>
                            <div className='flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-foreground'>
                                <span className='text-xs font-bold'>G</span>
                            </div>
                            <PageTitle pathname={location.pathname} />
                        </div>

                        <div className='hidden md:block'>
                            <Greeting />
                        </div>
                    </div>

                    <div className='flex items-center gap-2 sm:gap-3'>
                        <div className='hidden sm:block'>
                            <Input
                                aria-label='Search'
                                className='w-48 lg:w-72'
                                placeholder='Search nodes, sites, certs…'
                                variant='secondary'
                            />
                        </div>

                        <div className='hidden w-40 md:block lg:w-48'>
                            <ClusterPicker />
                        </div>

                        <Button
                            isIconOnly
                            aria-label='Create cluster'
                            className='hidden md:flex'
                            size='sm'
                            variant='ghost'
                            onPress={createModal.open}
                        >
                            <Plus className='h-4 w-4' />
                        </Button>

                        <Button
                            isIconOnly
                            className='hidden sm:flex'
                            size='sm'
                            variant='ghost'
                            onPress={toggleTheme}
                        >
                            {resolvedTheme === 'dark' ? (
                                <Sun className='h-4 w-4' />
                            ) : (
                                <Moon className='h-4 w-4' />
                            )}
                        </Button>

                        <Button isIconOnly className='hidden sm:flex' size='sm' variant='ghost'>
                            <Bell className='h-4 w-4' />
                        </Button>

                        <Avatar className='h-8 w-8 text-xs md:hidden'>
                            <Avatar.Fallback>{userInitial}</Avatar.Fallback>
                        </Avatar>
                    </div>
                </header>

                <main className='flex-1 overflow-y-auto bg-background p-4 md:p-6'>
                    <div className='mx-auto max-w-[1600px]'>
                        {clustersReady &&
                            !clusterId &&
                            !requiresCluster &&
                            location.pathname !== '/' && (
                                <Alert className='mb-6' status='warning'>
                                    <Alert.Indicator />
                                    <Alert.Content>
                                        <Alert.Title>No cluster selected</Alert.Title>
                                        <Alert.Description>
                                            Select a cluster in the header to use cluster features.
                                        </Alert.Description>
                                    </Alert.Content>
                                </Alert>
                            )}
                        <Outlet />
                    </div>
                </main>
            </div>

            <DialogShell
                isDismissable={!requiresCluster}
                isOpen={requiresCluster || createModal.isOpen}
                size='sm'
                subtitle={
                    requiresCluster
                        ? 'A cluster is required before you can add nodes, sites, or certificates.'
                        : 'Create an isolated workspace for nodes and sites.'
                }
                title={requiresCluster ? 'Create your first cluster' : 'Create cluster'}
                onOpenChange={(open) => {
                    if (!requiresCluster) createModal.setOpen(open);
                }}
            >
                <form className='flex flex-col' onSubmit={handleCreateCluster}>
                    <div className='space-y-4 p-6'>
                        {(createError || clusterError) && (
                            <FormError message={createError || clusterError || ''} />
                        )}
                        <FormField htmlFor='cluster-name' label='Cluster name' required>
                            <Input
                                autoFocus
                                id='cluster-name'
                                maxLength={80}
                                placeholder='Production edge'
                                required
                                variant='secondary'
                                value={clusterName}
                                onChange={(event) => setClusterName(event.target.value)}
                            />
                        </FormField>
                    </div>
                    <DialogFooter>
                        {!requiresCluster && (
                            <Button type='button' variant='ghost' onPress={createModal.close}>
                                Cancel
                            </Button>
                        )}
                        <Button isDisabled={creating || !clusterName.trim()} type='submit'>
                            {creating ? 'Creating…' : 'Create cluster'}
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <Button
                isIconOnly
                aria-label='Create cluster'
                className='fixed bottom-6 right-6 z-50 flex h-12 w-12 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-lg md:hidden'
                onPress={createModal.open}
            >
                <Plus className='h-5 w-5' />
            </Button>
        </div>
    );
}
