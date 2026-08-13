function pageKey(pathname: string) {
    const nodeDetail = pathname.match(/^\/nodes\/([^/]+)/);
    if (nodeDetail && nodeDetail[1] !== 'create') return `/nodes/${nodeDetail[1]}`;
    const siteDetail = pathname.match(/^\/sites\/([^/]+)/);
    if (siteDetail && siteDetail[1] !== 'create') return `/sites/${siteDetail[1]}`;
    if (/^\/settings\/admin(?:\/|$)/.test(pathname)) return '/settings/admin';
    return pathname;
}

export function clusterPageKey(clusterId: string, pathname: string) {
    return `${clusterId}:${pageKey(pathname)}`;
}
