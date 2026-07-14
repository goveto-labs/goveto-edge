import type { LoginRequest, RegisterRequest, User } from '@/api';

import {
    createContext,
    createElement,
    useCallback,
    useContext,
    useEffect,
    useMemo,
    useState,
} from 'react';

import { ApiError, authApi } from '@/api';

interface AuthContextValue {
    user: User | null;
    loading: boolean;
    error: string | null;
    login: (payload: LoginRequest) => Promise<void>;
    register: (payload: RegisterRequest) => Promise<void>;
    logout: () => void;
    refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
    const [user, setUser] = useState<User | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    const refresh = useCallback(async () => {
        setLoading(true);
        try {
            const me = await authApi.me();
            setUser(me);
            setError(null);
        } catch (err) {
            if (err instanceof ApiError && err.status === 401) {
                setUser(null);
            } else {
                setError(err instanceof Error ? err.message : 'Auth check failed');
            }
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        refresh();
    }, [refresh]);

    const login = useCallback(async (payload: LoginRequest) => {
        const loggedIn = await authApi.login(payload);
        setUser(loggedIn);
        setError(null);
    }, []);

    const register = useCallback(async (payload: RegisterRequest) => {
        await authApi.register(payload);
    }, []);

    const logout = useCallback(() => {
        setUser(null);
    }, []);

    const value = useMemo(
        () => ({
            user,
            loading,
            error,
            login,
            register,
            logout,
            refresh,
        }),
        [user, loading, error, login, register, logout, refresh]
    );

    return createElement(AuthContext.Provider, { value }, children);
}

export function useAuth() {
    const context = useContext(AuthContext);
    if (!context) {
        throw new Error('useAuth must be used within an AuthProvider');
    }
    return context;
}
