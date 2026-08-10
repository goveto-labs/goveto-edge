import { lazy, Suspense } from 'react';
import { Route, Routes } from 'react-router-dom';

import { Layout } from '@/components/Layout.tsx';
import { ProtectedRoute } from '@/components/ProtectedRoute.tsx';
import { ApiLoadingProvider } from '@/hooks/useApiLoading.tsx';
import { AuthProvider } from '@/hooks/useAuth.ts';
import { ClusterProvider } from '@/hooks/useCluster.ts';
import { InitializationGate, InitializationProvider } from '@/hooks/useInitialization.tsx';

const AdminSettings = lazy(() => import('@/pages/AdminSettings.tsx'));
const Analytics = lazy(() => import('@/pages/Analytics.tsx'));
const Certificates = lazy(() => import('@/pages/Certificates.tsx'));
const ClusterMembers = lazy(() => import('@/pages/ClusterMembers.tsx'));
const CreateNode = lazy(() => import('@/pages/CreateNode.tsx'));
const CreateSite = lazy(() => import('@/pages/CreateSite.tsx'));
const Dashboard = lazy(() => import('@/pages/Dashboard.tsx'));
const DNS = lazy(() => import('@/pages/DNS.tsx'));
const DNSZones = lazy(() => import('@/pages/DNSZones.tsx'));
const Init = lazy(() => import('@/pages/Init.tsx'));
const Jobs = lazy(() => import('@/pages/Jobs.tsx'));
const Login = lazy(() => import('@/pages/Login.tsx'));
const NodeDetail = lazy(() => import('@/pages/NodeDetail.tsx'));
const Nodes = lazy(() => import('@/pages/Nodes.tsx'));
const Notifications = lazy(() => import('@/pages/Notifications.tsx'));
const PurgeJobs = lazy(() => import('@/pages/PurgeJobs.tsx'));
const Register = lazy(() => import('@/pages/Register.tsx'));
const Settings = lazy(() => import('@/pages/Settings.tsx'));
const SiteDetail = lazy(() => import('@/pages/SiteDetail.tsx'));
const Sites = lazy(() => import('@/pages/Sites.tsx'));
const SitesAccessLogs = lazy(() => import('@/pages/SitesAccessLogs.tsx'));
const SSHCredentials = lazy(() => import('@/pages/SSHCredentials.tsx'));

function RouteFallback() {
    return (
        <div
            aria-label='Loading page'
            className='flex min-h-48 items-center justify-center text-sm text-muted'
            role='status'
        >
            Loading page…
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
                            <Suspense fallback={<RouteFallback />}>
                                <Routes>
                                    <Route element={<Init />} path='/init' />
                                    <Route element={<Login />} path='/login' />
                                    <Route element={<Register />} path='/register' />
                                    <Route
                                        element={
                                            <ProtectedRoute>
                                                <Layout />
                                            </ProtectedRoute>
                                        }
                                    >
                                        <Route element={<Dashboard />} path='/' />
                                        <Route element={<Nodes />} path='/nodes' />
                                        <Route element={<CreateNode />} path='/nodes/create' />
                                        <Route element={<NodeDetail />} path='/nodes/:nodeId/*' />
                                        <Route
                                            element={<SSHCredentials />}
                                            path='/nodes/ssh-credentials'
                                        />
                                        <Route element={<Sites />} path='/sites' />
                                        <Route element={<CreateSite />} path='/sites/create' />
                                        <Route element={<SitesAccessLogs />} path='/sites/logs' />
                                        <Route
                                            element={<Certificates />}
                                            path='/sites/certificates'
                                        />
                                        <Route element={<PurgeJobs />} path='/sites/cache' />
                                        <Route element={<SiteDetail />} path='/sites/:siteId/*' />
                                        <Route element={<DNS />} path='/dns' />
                                        <Route element={<DNSZones />} path='/dns/zones' />
                                        <Route element={<Certificates />} path='/certificates' />
                                        <Route element={<Jobs />} path='/jobs' />
                                        <Route element={<PurgeJobs />} path='/purge' />
                                        <Route element={<Analytics />} path='/analytics' />
                                        <Route element={<Settings />} path='/settings' />
                                        <Route
                                            element={<ClusterMembers />}
                                            path='/settings/members'
                                        />
                                        <Route
                                            element={<Notifications />}
                                            path='/settings/notifications'
                                        />
                                        <Route
                                            element={<AdminSettings />}
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
