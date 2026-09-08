import { expect, test } from 'vitest';

import { needsVersionUpgrade } from './version.ts';

test('version upgrade comparison follows SemVer ordering', () => {
    expect(needsVersionUpgrade(undefined, 'v1.2.3')).toBe(true);
    expect(needsVersionUpgrade('dev', 'v1.2.3')).toBe(true);
    expect(needsVersionUpgrade('1.2.2', 'v1.2.3')).toBe(true);
    expect(needsVersionUpgrade('1.2.3', 'v1.2.3')).toBe(false);
    expect(needsVersionUpgrade('v1.2.3+node.1', 'v1.2.3+control.2')).toBe(false);
    expect(needsVersionUpgrade('v1.2.3-rc.1', 'v1.2.3')).toBe(true);
    expect(needsVersionUpgrade('v1.3.0', 'v1.2.3')).toBe(false);
    expect(needsVersionUpgrade('v2.0.0', 'v1.9.9')).toBe(false);
});
