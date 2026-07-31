import { useEffect, useRef, useState } from 'react';
import { useLocation, useOutlet } from 'react-router-dom';

import { ApiSpinner } from '@/components/ApiSpinner.tsx';
import { LoadingSurface } from '@/components/LoadingSurface.tsx';
import { useApiLoading } from '@/hooks/useApiLoading.tsx';

function pageKey(pathname: string) {
    const nodeDetail = pathname.match(/^\/nodes\/([^/]+)/);
    if (nodeDetail && nodeDetail[1] !== 'create') return `/nodes/${nodeDetail[1]}`;
    const siteDetail = pathname.match(/^\/sites\/([^/]+)/);
    if (siteDetail && siteDetail[1] !== 'create') return `/sites/${siteDetail[1]}`;
    if (/^\/settings\/admin(?:\/|$)/.test(pathname)) return '/settings/admin';
    return pathname;
}

export function PageTransition() {
    const location = useLocation();
    const element = useOutlet();
    const { pending } = useApiLoading();
    const currentPageKey = pageKey(location.pathname);
    const usesLocalLoading =
        /^\/(?:nodes|sites)\/(?!create(?:\/|$))[^/]+(?:\/|$)/.test(location.pathname) ||
        /^\/settings\/admin(?:\/|$)/.test(location.pathname);
    const pendingRef = useRef(pending);
    const pageKeyRef = useRef(currentPageKey);
    pendingRef.current = pending;
    pageKeyRef.current = currentPageKey;

    const [revealedPageKey, setRevealedPageKey] = useState<string | null>(null);
    const rafRef = useRef<ReturnType<typeof requestAnimationFrame> | undefined>(undefined);
    const isVisible = revealedPageKey === currentPageKey;

    // A large-page key change hides the destination until its first requests
    // settle. Child routes within the same page stay visible and load locally.
    useEffect(() => {
        cancelAnimationFrame(rafRef.current ?? 0);
        if (pending > 0) return;

        const targetPageKey = currentPageKey;
        rafRef.current = requestAnimationFrame(() => {
            rafRef.current = requestAnimationFrame(() => {
                if (pendingRef.current === 0 && pageKeyRef.current === targetPageKey) {
                    setRevealedPageKey(targetPageKey);
                }
            });
        });
    }, [currentPageKey, pending]);

    useEffect(
        () => () => {
            cancelAnimationFrame(rafRef.current ?? 0);
        },
        []
    );

    return (
        <div className='relative min-h-full'>
            <ApiSpinner isLoading={!isVisible} />
            <div
                className={`min-h-full transition-opacity ease-out ${isVisible ? 'opacity-100' : 'pointer-events-none opacity-0'}`}
                style={{ transitionDuration: isVisible ? '200ms' : '0ms' }}
            >
                <LoadingSurface
                    className='min-h-full'
                    isLoading={isVisible && pending > 0 && !usesLocalLoading}
                    label='Updating page data'
                >
                    {element}
                </LoadingSurface>
            </div>
        </div>
    );
}
