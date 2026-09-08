import { describe, expect, test } from 'vitest';

import {
    enumField,
    integerField,
    parseListQuery,
    patchListQuery,
    serializeListQuery,
    stringField,
} from './listQuery.ts';

const schema = {
    q: stringField(),
    kind: enumField(['', 'PUBLISH', 'PURGE'] as const, ''),
    page: integerField(1, { min: 1 }),
    page_size: integerField(25, { allowed: [25, 50, 100] }),
};

describe('list query URL state', () => {
    test('omits default values from the serialized query string', () => {
        const params = serializeListQuery(
            { q: '', kind: '', page: 1, page_size: 25 },
            schema,
            new URLSearchParams('track=1')
        );
        expect(params.toString()).toBe('track=1');
    });

    test('round-trips non-default filters and preserves unrelated params', () => {
        const params = serializeListQuery(
            { q: 'origin', kind: 'PUBLISH', page: 3, page_size: 50 },
            schema,
            new URLSearchParams('cluster=edge')
        );
        expect(params.get('cluster')).toBe('edge');
        expect(parseListQuery(params, schema)).toEqual({
            q: 'origin',
            kind: 'PUBLISH',
            page: 3,
            page_size: 50,
        });
    });

    test('falls back to defaults for invalid enum, page, and page size values', () => {
        const params = new URLSearchParams('kind=UNKNOWN&page=0&page_size=13&q=cdn');
        expect(parseListQuery(params, schema)).toEqual({
            q: 'cdn',
            kind: '',
            page: 1,
            page_size: 25,
        });
    });

    test('resetPage restores the default page when filters change', () => {
        const current = new URLSearchParams('kind=PUBLISH&page=4&q=origin');
        const next = patchListQuery(current, schema, { kind: 'PURGE' }, { resetPage: true });
        expect(parseListQuery(next, schema)).toEqual({
            q: 'origin',
            kind: 'PURGE',
            page: 1,
            page_size: 25,
        });
    });
});
