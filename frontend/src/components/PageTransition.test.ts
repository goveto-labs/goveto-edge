import assert from 'node:assert/strict';
import test from 'node:test';

import { clusterPageKey } from './pageKey.ts';

test('switching clusters creates a new page instance key', () => {
    const clusterA = clusterPageKey('cluster-a', '/sites');
    const clusterB = clusterPageKey('cluster-b', '/sites');

    assert.notEqual(clusterA, clusterB);
});

test('detail tabs share one page instance within a cluster', () => {
    assert.equal(
        clusterPageKey('cluster-a', '/sites/site-1/cache'),
        clusterPageKey('cluster-a', '/sites/site-1/security')
    );
});
