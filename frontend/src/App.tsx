import { Route, Routes } from 'react-router-dom';

import { Layout } from '@/components/Layout.tsx';
import { ProtectedRoute } from '@/components/ProtectedRoute.tsx';
import { AuthProvider } from '@/hooks/useAuth.ts';
import { ClusterProvider } from '@/hooks/useCluster.ts';
import Analytics from '@/pages/Analytics.tsx';
import Certificates from '@/pages/Certificates.tsx';
import Dashboard from '@/pages/Dashboard.tsx';
import Login from '@/pages/Login.tsx';
import Nodes from '@/pages/Nodes.tsx';
import PublishJobs from '@/pages/PublishJobs.tsx';
import PurgeJobs from '@/pages/PurgeJobs.tsx';
import Register from '@/pages/Register.tsx';
import Sites from '@/pages/Sites.tsx';

export default function App() {
    return (
        <AuthProvider>
            <ClusterProvider>
                <Routes>
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
                        <Route element={<Sites />} path='/sites' />
                        <Route element={<Certificates />} path='/certificates' />
                        <Route element={<PublishJobs />} path='/publish' />
                        <Route element={<PurgeJobs />} path='/purge' />
                        <Route element={<Analytics />} path='/analytics' />
                    </Route>
                </Routes>
            </ClusterProvider>
        </AuthProvider>
    );
}
