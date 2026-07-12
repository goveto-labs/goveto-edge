import { Button, Input, useTheme } from '@heroui/react';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';

import { useAuth } from '@/hooks/useAuth.ts';
import { useCluster } from '@/hooks/useCluster.ts';

const nav = [
    { path: '/', label: 'Dashboard' },
    { path: '/nodes', label: 'Nodes' },
    { path: '/sites', label: 'Sites' },
    { path: '/certificates', label: 'Certificates' },
    { path: '/publish', label: 'Publish Jobs' },
    { path: '/purge', label: 'Purge Jobs' },
    { path: '/analytics', label: 'Analytics' },
];

export function Layout() {
    const navigate = useNavigate();
    const location = useLocation();
    const { user, logout } = useAuth();
    const { clusterId, setClusterId } = useCluster();
    const { resolvedTheme, setTheme } = useTheme();

    const toggleTheme = () => {
        setTheme(resolvedTheme === 'dark' ? 'light' : 'dark');
    };

    const handleLogout = () => {
        logout();
        navigate('/login');
    };

    return (
        <div className='flex h-full flex-col'>
            <header className='flex items-center justify-between gap-4 border-b border-border bg-surface px-4 py-3'>
                <div className='flex items-center gap-4'>
                    <Link className='text-xl font-bold' to='/'>
                        Goveto Edge
                    </Link>
                    <div className='hidden items-center gap-2 md:flex'>
                        <span className='text-sm text-muted'>Cluster:</span>
                        <Input
                            aria-label='Cluster ID'
                            className='w-64'
                            placeholder='cluster-id'
                            value={clusterId}
                            onChange={(e) => setClusterId(e.target.value)}
                        />
                    </div>
                </div>

                <div className='flex items-center gap-3'>
                    <Button size='sm' variant='ghost' onPress={toggleTheme}>
                        {resolvedTheme === 'dark' ? 'Light' : 'Dark'}
                    </Button>
                    <div className='hidden text-sm md:block'>
                        {user?.name || user?.email || user?.id}
                    </div>
                    <Button size='sm' variant='secondary' onPress={handleLogout}>
                        Logout
                    </Button>
                </div>
            </header>

            <div className='flex flex-1 overflow-hidden'>
                <aside className='hidden w-56 overflow-y-auto border-r border-border bg-surface-secondary md:block'>
                    <nav className='space-y-1 p-3'>
                        {nav.map((item) => {
                            const active = location.pathname === item.path;
                            return (
                                <Link
                                    key={item.path}
                                    className={`block rounded-md px-3 py-2 text-sm transition ${
                                        active
                                            ? 'bg-accent text-accent-foreground'
                                            : 'hover:bg-surface-tertiary'
                                    }`}
                                    to={item.path}
                                >
                                    {item.label}
                                </Link>
                            );
                        })}
                    </nav>
                </aside>

                <main className='flex-1 overflow-y-auto p-4 md:p-6'>
                    {!clusterId && (
                        <div className='mb-4 rounded-md bg-warning p-3 text-sm text-warning-foreground'>
                            No cluster selected. Enter a cluster ID in the header to use cluster
                            features.
                        </div>
                    )}
                    <Outlet />
                </main>
            </div>
        </div>
    );
}
