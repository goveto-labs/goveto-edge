import { lazy } from 'react';

/**
 * Centralized route chunk loaders.
 *
 * Each loader is shared between `React.lazy` (for rendering) and the preload
 * helpers (for warming the cache ahead of navigation). Preloading is what
 * eliminates the per-page "Loading page…" flash: by the time the user clicks a
 * nav item, the target chunk is already in the browser cache, so the route
 * mounts without hitting the Suspense fallback.
 */

const loaders = {
    AdminSettings: () => import('@/pages/AdminSettings.tsx'),
    Analytics: () => import('@/pages/Analytics.tsx'),
    Certificates: () => import('@/pages/Certificates.tsx'),
    ClusterMembers: () => import('@/pages/ClusterMembers.tsx'),
    CreateNode: () => import('@/pages/CreateNode.tsx'),
    CreateSite: () => import('@/pages/CreateSite.tsx'),
    Dashboard: () => import('@/pages/Dashboard.tsx'),
    DNS: () => import('@/pages/DNS.tsx'),
    DNSZones: () => import('@/pages/DNSZones.tsx'),
    Init: () => import('@/pages/Init.tsx'),
    Jobs: () => import('@/pages/Jobs.tsx'),
    Login: () => import('@/pages/Login.tsx'),
    NodeDetail: () => import('@/pages/NodeDetail.tsx'),
    Nodes: () => import('@/pages/Nodes.tsx'),
    Notifications: () => import('@/pages/Notifications.tsx'),
    PurgeJobs: () => import('@/pages/PurgeJobs.tsx'),
    Register: () => import('@/pages/Register.tsx'),
    Settings: () => import('@/pages/Settings.tsx'),
    SiteDetail: () => import('@/pages/SiteDetail.tsx'),
    Sites: () => import('@/pages/Sites.tsx'),
    SitesAccessLogs: () => import('@/pages/SitesAccessLogs.tsx'),
    SSHCredentials: () => import('@/pages/SSHCredentials.tsx'),
} as const;

export const lazyRoutes = {
    AdminSettings: lazy(loaders.AdminSettings),
    Analytics: lazy(loaders.Analytics),
    Certificates: lazy(loaders.Certificates),
    ClusterMembers: lazy(loaders.ClusterMembers),
    CreateNode: lazy(loaders.CreateNode),
    CreateSite: lazy(loaders.CreateSite),
    Dashboard: lazy(loaders.Dashboard),
    DNS: lazy(loaders.DNS),
    DNSZones: lazy(loaders.DNSZones),
    Init: lazy(loaders.Init),
    Jobs: lazy(loaders.Jobs),
    Login: lazy(loaders.Login),
    NodeDetail: lazy(loaders.NodeDetail),
    Nodes: lazy(loaders.Nodes),
    Notifications: lazy(loaders.Notifications),
    PurgeJobs: lazy(loaders.PurgeJobs),
    Register: lazy(loaders.Register),
    Settings: lazy(loaders.Settings),
    SiteDetail: lazy(loaders.SiteDetail),
    Sites: lazy(loaders.Sites),
    SitesAccessLogs: lazy(loaders.SitesAccessLogs),
    SSHCredentials: lazy(loaders.SSHCredentials),
};

/**
 * Static nav path → loader map, used for intent-based prefetching on hover /
 * focus. Dynamic detail routes (`/nodes/:id`, `/sites/:id`) are intentionally
 * omitted here; they are still warmed by {@link preloadAllRoutes}.
 */
const routeLoaders: Record<string, () => Promise<unknown>> = {
    '/': loaders.Dashboard,
    '/nodes': loaders.Nodes,
    '/nodes/create': loaders.CreateNode,
    '/nodes/ssh-credentials': loaders.SSHCredentials,
    '/sites': loaders.Sites,
    '/sites/create': loaders.CreateSite,
    '/sites/logs': loaders.SitesAccessLogs,
    '/sites/certificates': loaders.Certificates,
    '/sites/cache': loaders.PurgeJobs,
    '/dns': loaders.DNS,
    '/dns/zones': loaders.DNSZones,
    '/certificates': loaders.Certificates,
    '/jobs': loaders.Jobs,
    '/purge': loaders.PurgeJobs,
    '/analytics': loaders.Analytics,
    '/settings': loaders.Settings,
    '/settings/members': loaders.ClusterMembers,
    '/settings/notifications': loaders.Notifications,
    '/settings/admin': loaders.AdminSettings,
};

const preloaded = new Set<() => Promise<unknown>>();

function runLoader(loader: () => Promise<unknown>) {
    if (preloaded.has(loader)) return;
    preloaded.add(loader);
    void loader().catch(() => {
        // Allow a retry on transient failure (e.g. an offline blip).
        preloaded.delete(loader);
    });
}

/** Warm a single route chunk by path (used on hover/focus). */
export function preloadRoute(path: string) {
    const loader = routeLoaders[path];
    if (loader) runLoader(loader);
}

/** Warm every page chunk so navigation never hits the Suspense fallback. */
export function preloadAllRoutes() {
    Object.values(loaders).forEach(runLoader);
}

/**
 * Schedule a task during browser idle time. Falls back to a short timeout when
 * `requestIdleCallback` is unavailable. Returns a cancel function.
 */
export function scheduleIdle(task: () => void, timeout = 4000): () => void {
    if (typeof window !== 'undefined' && typeof window.requestIdleCallback === 'function') {
        const handle = window.requestIdleCallback(task, { timeout });
        return () => window.cancelIdleCallback(handle);
    }
    const timer = window.setTimeout(task, 1500);
    return () => window.clearTimeout(timer);
}
