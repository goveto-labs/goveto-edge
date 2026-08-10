import { useEffect, useRef, useState } from 'react';
import { useLocation, useOutlet } from 'react-router-dom';

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

/**
 * Full-content loading state shown while a route switch is in flight. Renders
 * immediately (no fade-in) so there is never a blank gap between the previous
 * page hiding and the destination's data arriving. Visually consistent with
 * the in-frame {@link ContentFallback} Suspense boundary in Layout.
 *
 * This is distinct from {@link LoadingSurface}, whose translucent overlay +
 * fade-in is meant for refreshing content that is already on screen. During a
 * route switch the destination is opacity-0, so a fading 55% veil would be
 * invisible against the empty background — hence the dedicated solid spinner.
 */
function PageSwitchOverlay() {
    return (
        <div className='pointer-events-none absolute inset-0 z-50 flex min-h-[60vh] items-center justify-center'>
            <div className='flex flex-col items-center gap-3 text-muted'>
                <div className='h-6 w-6 animate-spin rounded-full border-2 border-current border-t-transparent' />
                <span className='text-xs'>Loading page…</span>
            </div>
        </div>
    );
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

    const isSwitching = !isVisible;
    const isUpdating = isVisible && pending > 0 && !usesLocalLoading;

    return (
        <div className='relative min-h-full'>
            <LoadingSurface
                className='min-h-full'
                isLoading={isUpdating}
                label='Updating page data'
            >
                <div
                    className={`min-h-full transition-opacity ease-out ${isVisible ? 'opacity-100' : 'pointer-events-none opacity-0'}`}
                    style={{ transitionDuration: isVisible ? '200ms' : '0ms' }}
                >
                    {element}
                </div>
            </LoadingSurface>
            {isSwitching && <PageSwitchOverlay />}
        </div>
    );
}
