import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import {
    parseListQuery,
    patchListQuery,
    type QuerySchema,
    type QueryValues,
} from '@/utils/listQuery.ts';

interface UseListQueryOptions<S extends QuerySchema> {
    searchKey?: Extract<keyof S, string>;
    debounceMs?: number;
}

export function useListQuery<S extends QuerySchema>(
    schema: S,
    options: UseListQueryOptions<S> = {}
) {
    const [params, setParams] = useSearchParams();
    const values = useMemo(() => parseListQuery(params, schema), [params, schema]);
    const searchKey = options.searchKey;
    const committedSearch = searchKey ? String(values[searchKey] ?? '') : '';
    const [searchInput, setSearchInput] = useState(() => committedSearch);

    useEffect(() => {
        if (!searchKey) return;
        setSearchInput(committedSearch);
    }, [committedSearch, searchKey]);

    const replace = useCallback(
        (patch: Partial<QueryValues<S>>, replaceOptions?: { resetPage?: boolean }) => {
            setParams((current) => patchListQuery(current, schema, patch, replaceOptions));
        },
        [schema, setParams]
    );

    useEffect(() => {
        if (!searchKey) return;
        const timer = window.setTimeout(() => {
            const next = searchInput.trim();
            if (next === committedSearch) return;
            replace({ [searchKey]: next } as Partial<QueryValues<S>>, { resetPage: true });
        }, options.debounceMs ?? 300);
        return () => window.clearTimeout(timer);
    }, [committedSearch, options.debounceMs, replace, searchInput, searchKey]);

    return { values, replace, searchInput, setSearchInput };
}
