import assert from 'node:assert/strict';
import test from 'node:test';

import { needsVersionUpgrade } from './version.ts';

test('version upgrade comparison follows SemVer ordering', () => {
    assert.equal(needsVersionUpgrade(undefined, 'v1.2.3'), true);
    assert.equal(needsVersionUpgrade('dev', 'v1.2.3'), true);
    assert.equal(needsVersionUpgrade('1.2.2', 'v1.2.3'), true);
    assert.equal(needsVersionUpgrade('1.2.3', 'v1.2.3'), false);
    assert.equal(needsVersionUpgrade('v1.2.3+node.1', 'v1.2.3+control.2'), false);
    assert.equal(needsVersionUpgrade('v1.2.3-rc.1', 'v1.2.3'), true);
    assert.equal(needsVersionUpgrade('v1.3.0', 'v1.2.3'), false);
    assert.equal(needsVersionUpgrade('v2.0.0', 'v1.9.9'), false);
});
