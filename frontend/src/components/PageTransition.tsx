import { useEffect, useRef, useState } from 'react';
import { useLocation, useOutlet } from 'react-router-dom';

import { useApiLoading } from '@/hooks/useApiLoading.tsx';

export function PageTransition() {
    const location = useLocation();
    const element = useOutlet();
    const { pending } = useApiLoading();
    const pendingRef = useRef(pending);
    const routeKeyRef = useRef(location.key);
    pendingRef.current = pending;
    routeKeyRef.current = location.key;

    const [revealedRouteKey, setRevealedRouteKey] = useState<string | null>(null);
    const rafRef = useRef<ReturnType<typeof requestAnimationFrame> | undefined>(undefined);
    const isVisible = revealedRouteKey === location.key;

    // The route key changes during render, so the destination is hidden in the
    // same browser frame in which it replaces the previous page. Once its first
    // requests settle, reveal only that route with a fade-in.
    useEffect(() => {
        cancelAnimationFrame(rafRef.current ?? 0);
        if (pending > 0) return;

        const routeKey = location.key;
        rafRef.current = requestAnimationFrame(() => {
            rafRef.current = requestAnimationFrame(() => {
                if (pendingRef.current === 0 && routeKeyRef.current === routeKey) {
                    setRevealedRouteKey(routeKey);
                }
            });
        });
    }, [location.key, pending]);

    useEffect(
        () => () => {
            cancelAnimationFrame(rafRef.current ?? 0);
        },
        []
    );

    return (
        <div
            className={`transition-opacity ease-out ${isVisible ? 'opacity-100' : 'pointer-events-none opacity-0'}`}
            style={{ transitionDuration: isVisible ? '200ms' : '0ms' }}
        >
            {element}
        </div>
    );
}
