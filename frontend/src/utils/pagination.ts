export type PageItem = number | 'start-ellipsis' | 'end-ellipsis';

export function visiblePages(current: number, total: number): PageItem[] {
    if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1);
    if (current <= 4) return [1, 2, 3, 4, 5, 'end-ellipsis', total];
    if (current >= total - 3) {
        return [1, 'start-ellipsis', total - 4, total - 3, total - 2, total - 1, total];
    }
    return [1, 'start-ellipsis', current - 1, current, current + 1, 'end-ellipsis', total];
}

export function pageRange(page: number, pageSize: number, total: number) {
    const pageCount = Math.max(1, Math.ceil((total || 0) / Math.max(1, pageSize)));
    const safePage = Math.min(Math.max(1, page), pageCount);
    const start = total === 0 ? 0 : (safePage - 1) * pageSize + 1;
    const end = Math.min(safePage * pageSize, total);
    return { page: safePage, pageCount, start, end };
}

export function paginateItems<T>(items: T[], page: number, pageSize: number) {
    const total = items.length;
    const range = pageRange(page, pageSize, total);
    const from = range.start === 0 ? 0 : range.start - 1;
    return {
        items: items.slice(from, range.end),
        total,
        ...range,
    };
}

export function matchesQuery(
    query: string,
    ...fields: Array<string | number | undefined | null | readonly string[]>
) {
    const needle = query.trim().toLocaleLowerCase();
    if (!needle) return true;
    return fields.some((field) => {
        if (Array.isArray(field)) {
            return field.some((value) => value.toLocaleLowerCase().includes(needle));
        }
        return String(field ?? '')
            .toLocaleLowerCase()
            .includes(needle);
    });
}
