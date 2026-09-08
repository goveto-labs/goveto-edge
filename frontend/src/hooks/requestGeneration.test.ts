import { expect, test } from 'vitest';

import { RequestGeneration } from './requestGeneration.ts';

test('a late request cannot commit after a newer request starts', () => {
    const requests = new RequestGeneration();
    const firstController = new AbortController();
    const secondController = new AbortController();

    const first = requests.next();
    const second = requests.next();

    expect(requests.isCurrent(first, firstController.signal)).toBe(false);
    expect(requests.isCurrent(second, secondController.signal)).toBe(true);
});

test('invalidating a request prevents it from committing after cleanup', () => {
    const requests = new RequestGeneration();
    const controller = new AbortController();
    const request = requests.next();

    requests.invalidate();
    controller.abort();

    expect(requests.isCurrent(request, controller.signal)).toBe(false);
});
