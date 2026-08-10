import { Suspense } from 'react';
import { Route, Routes } from 'react-router-dom';

import { Layout } from '@/components/Layout.tsx';
import { ProtectedRoute } from '@/components/ProtectedRoute.tsx';
import { ApiLoadingProvider } from '@/hooks/useApiLoading.tsx';
import { AuthProvider } from '@/hooks/useAuth.ts';
import { ClusterProvider } from '@/hooks/useCluster.ts';
import { InitializationGate, InitializationProvider } from '@/hooks/useInitialization.tsx';
import { lazyRoutes } from '@/routes.tsx';

/**
 * Top-level fallback for the pre-auth entry routes (init / login / register).
 * These are hit on a cold load before any preloading can run, so a full-viewport
 * spinner matches the existing {@link InitializationGate} boot experience. The
 * primary authenticated routes live under {@link Layout}, which owns its own
 * in-frame Suspense boundary so their fallback renders inside the app shell.
 */
function BootFallback() {
    return (
        <div className='flex min-h-[100dvh] items-center justify-center bg-background'>
            <div className='flex flex-col items-center gap-3 text-muted'>
                <div className='h-7 w-7 animate-spin rounded-full border-2 border-current border-t-transparent' />
                <span className='text-sm'>Loading…</span>
            </div>
        </div>
    );
}

export default function App() {
    return (
        <ApiLoadingProvider>
            <InitializationProvider>
                <InitializationGate>
                    <AuthProvider>
                        <ClusterProvider>
                            <Suspense fallback={<BootFallback />}>
                                <Routes>
                                    <Route element={<lazyRoutes.Init />} path='/init' />
                                    <Route element={<lazyRoutes.Login />} path='/login' />
                                    <Route element={<lazyRoutes.Register />} path='/register' />
                                    <Route
                                        element={
                                            <ProtectedRoute>
                                                <Layout />
                                            </ProtectedRoute>
                                        }
                                    >
                                        <Route element={<lazyRoutes.Dashboard />} path='/' />
                                        <Route element={<lazyRoutes.Nodes />} path='/nodes' />
                                        <Route
                                            element={<lazyRoutes.CreateNode />}
                                            path='/nodes/create'
                                        />
                                        <Route
                                            element={<lazyRoutes.NodeDetail />}
                                            path='/nodes/:nodeId/*'
                                        />
                                        <Route
                                            element={<lazyRoutes.SSHCredentials />}
                                            path='/nodes/ssh-credentials'
                                        />
                                        <Route element={<lazyRoutes.Sites />} path='/sites' />
                                        <Route
                                            element={<lazyRoutes.CreateSite />}
                                            path='/sites/create'
                                        />
                                        <Route
                                            element={<lazyRoutes.SitesAccessLogs />}
                                            path='/sites/logs'
                                        />
                                        <Route
                                            element={<lazyRoutes.Certificates />}
                                            path='/sites/certificates'
                                        />
                                        <Route
                                            element={<lazyRoutes.PurgeJobs />}
                                            path='/sites/cache'
                                        />
                                        <Route
                                            element={<lazyRoutes.SiteDetail />}
                                            path='/sites/:siteId/*'
                                        />
                                        <Route element={<lazyRoutes.DNS />} path='/dns' />
                                        <Route
                                            element={<lazyRoutes.DNSZones />}
                                            path='/dns/zones'
                                        />
                                        <Route
                                            element={<lazyRoutes.Certificates />}
                                            path='/certificates'
                                        />
                                        <Route element={<lazyRoutes.Jobs />} path='/jobs' />
                                        <Route element={<lazyRoutes.PurgeJobs />} path='/purge' />
                                        <Route
                                            element={<lazyRoutes.Analytics />}
                                            path='/analytics'
                                        />
                                        <Route element={<lazyRoutes.Settings />} path='/settings' />
                                        <Route
                                            element={<lazyRoutes.ClusterMembers />}
                                            path='/settings/members'
                                        />
                                        <Route
                                            element={<lazyRoutes.Notifications />}
                                            path='/settings/notifications'
                                        />
                                        <Route
                                            element={<lazyRoutes.AdminSettings />}
                                            path='/settings/admin/*'
                                        />
                                    </Route>
                                </Routes>
                            </Suspense>
                        </ClusterProvider>
                    </AuthProvider>
                </InitializationGate>
            </InitializationProvider>
        </ApiLoadingProvider>
    );
}
