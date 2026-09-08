import { expect, test } from 'vitest';

import { clusterPageKey } from './pageKey.ts';

test('switching clusters creates a new page instance key', () => {
    const clusterA = clusterPageKey('cluster-a', '/sites');
    const clusterB = clusterPageKey('cluster-b', '/sites');

    expect(clusterA).not.toBe(clusterB);
});

test('detail tabs share one page instance within a cluster', () => {
    expect(clusterPageKey('cluster-a', '/sites/site-1/cache')).toBe(
        clusterPageKey('cluster-a', '/sites/site-1/security')
    );
});
