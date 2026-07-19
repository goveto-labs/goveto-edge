import { Route, Routes } from 'react-router-dom';

import { Layout } from '@/components/Layout.tsx';
import { ProtectedRoute } from '@/components/ProtectedRoute.tsx';
import { ApiLoadingProvider } from '@/hooks/useApiLoading.tsx';
import { AuthProvider } from '@/hooks/useAuth.ts';
import { ClusterProvider } from '@/hooks/useCluster.ts';
import { InitializationGate, InitializationProvider } from '@/hooks/useInitialization.tsx';
import Analytics from '@/pages/Analytics.tsx';
import Certificates from '@/pages/Certificates.tsx';
import CreateNode from '@/pages/CreateNode.tsx';
import CreateSite from '@/pages/CreateSite.tsx';
import Dashboard from '@/pages/Dashboard.tsx';
import DNS from '@/pages/DNS.tsx';
import Init from '@/pages/Init.tsx';
import Login from '@/pages/Login.tsx';
import NodeDetail from '@/pages/NodeDetail.tsx';
import Nodes from '@/pages/Nodes.tsx';
import PublishJobs from '@/pages/PublishJobs.tsx';
import PurgeJobs from '@/pages/PurgeJobs.tsx';
import Register from '@/pages/Register.tsx';
import SiteDetail from '@/pages/SiteDetail.tsx';
import Sites from '@/pages/Sites.tsx';

export default function App() {
    return (
        <ApiLoadingProvider>
            <InitializationProvider>
                <InitializationGate>
                    <AuthProvider>
                        <ClusterProvider>
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
                                    <Route element={<Sites />} path='/sites' />
                                    <Route element={<CreateSite />} path='/sites/create' />
                                    <Route element={<SiteDetail />} path='/sites/:siteId/*' />
                                    <Route element={<DNS />} path='/dns' />
                                    <Route element={<Certificates />} path='/certificates' />
                                    <Route element={<PublishJobs />} path='/publish' />
                                    <Route element={<PurgeJobs />} path='/purge' />
                                    <Route element={<Analytics />} path='/analytics' />
                                </Route>
                            </Routes>
                        </ClusterProvider>
                    </AuthProvider>
                </InitializationGate>
            </InitializationProvider>
        </ApiLoadingProvider>
    );
}
