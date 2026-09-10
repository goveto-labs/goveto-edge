import { useEffect, useRef, useState } from 'react';
import { useLocation, useOutlet } from 'react-router-dom';

import { LoadingSurface } from '@/components/LoadingSurface.tsx';
import { clusterPageKey } from '@/components/pageKey.ts';
import { useApiLoading } from '@/hooks/useApiLoading.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

/**
 * Compact route-switch indicator pinned to the top-left corner. Renders
 * immediately (no fade-in) so there is never a blank gap between the previous
 * page hiding and the destination's data arriving. Because it is anchored to
 * the corner it does not depend on the container height, which avoids the
 * spinner shifting around as different pages' empty shells render at
 * different heights.
 *
 * This is distinct from {@link LoadingSurface}, whose translucent overlay +
 * fade-in is meant for refreshing content that is already on screen. During a
 * route switch the destination is opacity-0, so a fading 55% veil would be
 * invisible against the empty background — hence this dedicated spinner.
 */
function PageSwitchOverlay() {
    return (
        <div className='pointer-events-none absolute left-0 top-0 z-50 p-1 text-accent'>
            <div className='h-5 w-5 animate-spin rounded-full border-2 border-current border-t-transparent' />
        </div>
    );
}

export function PageTransition() {
    const location = useLocation();
    const element = useOutlet();
    const { pending } = useApiLoading();
    const { clusterId } = useCluster();
    const currentPageKey = clusterPageKey(clusterId, location.pathname);
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
                    key={currentPageKey}
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
