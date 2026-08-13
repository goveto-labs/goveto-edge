import assert from 'node:assert/strict';
import test from 'node:test';

import { RequestGeneration } from './requestGeneration.ts';

test('a late request cannot commit after a newer request starts', () => {
    const requests = new RequestGeneration();
    const firstController = new AbortController();
    const secondController = new AbortController();

    const first = requests.next();
    const second = requests.next();

    assert.equal(requests.isCurrent(first, firstController.signal), false);
    assert.equal(requests.isCurrent(second, secondController.signal), true);
});

test('invalidating a request prevents it from committing after cleanup', () => {
    const requests = new RequestGeneration();
    const controller = new AbortController();
    const request = requests.next();

    requests.invalidate();
    controller.abort();

    assert.equal(requests.isCurrent(request, controller.signal), false);
});
