import { describe, expect, test } from 'vitest';

import { matchesQuery, pageRange, paginateItems, visiblePages } from './pagination.ts';

describe('list pagination helpers', () => {
    test('visible page windows keep the first and last pages', () => {
        expect(visiblePages(1, 4)).toEqual([1, 2, 3, 4]);
        expect(visiblePages(2, 10)).toEqual([1, 2, 3, 4, 5, 'end-ellipsis', 10]);
        expect(visiblePages(8, 10)).toEqual([1, 'start-ellipsis', 6, 7, 8, 9, 10]);
        expect(visiblePages(5, 10)).toEqual([1, 'start-ellipsis', 4, 5, 6, 'end-ellipsis', 10]);
    });

    test('pageRange clamps oversized pages and empty lists', () => {
        expect(pageRange(9, 25, 40)).toEqual({ page: 2, pageCount: 2, start: 26, end: 40 });
        expect(pageRange(1, 25, 0)).toEqual({ page: 1, pageCount: 1, start: 0, end: 0 });
    });

    test('paginateItems slices the current page after clamping', () => {
        const items = ['a', 'b', 'c', 'd', 'e'];
        expect(paginateItems(items, 2, 2).items).toEqual(['c', 'd']);
        expect(paginateItems(items, 9, 2)).toEqual({
            items: ['e'],
            total: 5,
            page: 3,
            pageCount: 3,
            start: 5,
            end: 5,
        });
    });

    test('matchesQuery searches across scalar and list fields', () => {
        expect(matchesQuery('goveto', 'Goveto Edge', ['app.example.com'])).toBe(true);
        expect(matchesQuery('example', 'Goveto Edge', ['app.example.com'])).toBe(true);
        expect(matchesQuery('origin', 'site-1', ['cdn.example.net'])).toBe(false);
        expect(matchesQuery('  ', 'anything')).toBe(true);
    });
});
