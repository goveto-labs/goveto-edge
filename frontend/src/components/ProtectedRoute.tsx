import { Spinner } from '@heroui/react';
import { Navigate } from 'react-router-dom';

import { useAuth } from '@/hooks/useAuth.ts';

export function ProtectedRoute({ children }: { children: React.ReactNode }) {
    const { user, loading } = useAuth();

    if (loading) {
        return (
            <div className='flex h-screen items-center justify-center'>
                <Spinner size='lg' />
            </div>
        );
    }

    if (!user) {
        return <Navigate replace to='/login' />;
    }

    return <>{children}</>;
}
