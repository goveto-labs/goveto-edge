import type { LucideIcon } from 'lucide-react';

import {
    Alert,
    Avatar,
    Button,
    Drawer,
    Dropdown,
    Input,
    Label,
    ListBox,
    Modal,
    Select,
    useOverlayState,
    useTheme,
} from '@heroui/react';
import {
    BarChart3,
    ChevronDown,
    Cloud,
    Globe,
    LayoutDashboard,
    LogOut,
    Menu,
    Moon,
    Plus,
    Rocket,
    Server,
    ShieldCheck,
    Sun,
    Trash2,
} from 'lucide-react';
import { useState } from 'react';
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
    { path: '/dns', label: 'DNS', icon: Cloud },
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
    const {
        clusterId,
        clusters,
        loading: clustersLoading,
        error: clusterError,
        requiresCluster,
        setClusterId,
        createCluster,
    } = useCluster();
    const { resolvedTheme, setTheme } = useTheme();
    const mobileMenu = useOverlayState();
    const createModal = useOverlayState();
    const [clusterName, setClusterName] = useState('');
    const [creating, setCreating] = useState(false);
    const [createError, setCreateError] = useState<string | null>(null);

    const toggleTheme = () => {
        setTheme(resolvedTheme === 'dark' ? 'light' : 'dark');
    };

    const handleLogout = () => {
        logout();
        navigate('/login');
    };

    const userLabel = user?.name || user?.email || user?.id || 'User';
    const userInitial = userLabel.slice(0, 1).toUpperCase();

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

    return (
        <div className='flex h-full'>
            <aside className='hidden w-64 flex-col overflow-y-auto border-r border-border bg-surface md:flex'>
                <SidebarContent />
            </aside>

            <div className='flex min-w-0 flex-1 flex-col'>
                <header className='flex h-16 shrink-0 items-center justify-between gap-4 border-b border-border bg-surface px-4'>
                    <div className='flex items-center gap-3'>
                        <Drawer state={mobileMenu}>
                            <Drawer.Trigger aria-label='Open navigation' className='md:hidden'>
                                <Menu className='h-5 w-5' />
                            </Drawer.Trigger>
                            <Drawer.Content className='w-[280px]'>
                                <SidebarContent onNavigate={mobileMenu.close} />
                            </Drawer.Content>
                        </Drawer>
                    </div>

                    <div className='flex flex-1 items-center justify-end gap-3'>
                        <div className='flex items-center gap-2'>
                            <span className='hidden text-sm text-muted md:inline'>Cluster</span>
                            <Select
                                aria-label='Current cluster'
                                className='w-36 sm:w-48 md:w-64'
                                value={clusterId}
                                onChange={(key) => {
                                    if (key) void setClusterId(String(key));
                                }}
                            >
                                <Select.Trigger>
                                    <Select.Value>
                                        {clustersLoading ? 'Loading clusters…' : undefined}
                                    </Select.Value>
                                </Select.Trigger>
                                <Select.Popover>
                                    <ListBox>
                                        {clusters.map((cluster) => (
                                            <ListBox.Item
                                                id={cluster.id}
                                                key={cluster.id}
                                                textValue={cluster.name}
                                            >
                                                {cluster.name}
                                            </ListBox.Item>
                                        ))}
                                    </ListBox>
                                </Select.Popover>
                            </Select>
                            <Modal
                                isOpen={requiresCluster || createModal.isOpen}
                                onOpenChange={(open) => {
                                    if (!requiresCluster) createModal.setOpen(open);
                                }}
                            >
                                <Modal.Trigger
                                    aria-label='Create cluster'
                                    className='rounded-lg p-2 text-muted transition-colors hover:bg-surface-secondary hover:text-foreground'
                                >
                                    <Plus className='h-4 w-4' />
                                </Modal.Trigger>
                                <Modal.Backdrop isDismissable={!requiresCluster}>
                                    <Modal.Container placement='center' size='sm'>
                                        <Modal.Dialog>
                                            <form
                                                className='space-y-4'
                                                onSubmit={handleCreateCluster}
                                            >
                                                <Modal.Header>
                                                    <Modal.Heading>
                                                        {requiresCluster
                                                            ? 'Create your first cluster'
                                                            : 'Create cluster'}
                                                    </Modal.Heading>
                                                </Modal.Header>
                                                <Modal.Body className='space-y-4'>
                                                    <p className='text-sm text-muted'>
                                                        {requiresCluster
                                                            ? 'A cluster is required before you can add nodes, sites, or certificates.'
                                                            : 'Create an isolated workspace for nodes and sites.'}
                                                    </p>
                                                    {(createError || clusterError) && (
                                                        <div
                                                            className='rounded-xl border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger-foreground'
                                                            role='alert'
                                                        >
                                                            {createError || clusterError}
                                                        </div>
                                                    )}
                                                    <div className='flex flex-col gap-1'>
                                                        <Label htmlFor='cluster-name'>
                                                            Cluster name
                                                        </Label>
                                                        <Input
                                                            autoFocus
                                                            variant='secondary'
                                                            id='cluster-name'
                                                            maxLength={80}
                                                            placeholder='Production edge'
                                                            required
                                                            value={clusterName}
                                                            onChange={(event) =>
                                                                setClusterName(event.target.value)
                                                            }
                                                        />
                                                    </div>
                                                </Modal.Body>
                                                <Modal.Footer>
                                                    {!requiresCluster && (
                                                        <Button
                                                            variant='ghost'
                                                            onPress={createModal.close}
                                                        >
                                                            Cancel
                                                        </Button>
                                                    )}
                                                    <Button
                                                        isDisabled={creating || !clusterName.trim()}
                                                        type='submit'
                                                    >
                                                        {creating ? 'Creating…' : 'Create cluster'}
                                                    </Button>
                                                </Modal.Footer>
                                            </form>
                                        </Modal.Dialog>
                                    </Modal.Container>
                                </Modal.Backdrop>
                            </Modal>
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
                    {!clusterId && !requiresCluster && location.pathname !== '/' && (
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
                </main>
            </div>
        </div>
    );
}
