export type StringQueryField = { kind: 'string'; default: string };
export type IntegerQueryField = {
    kind: 'integer';
    default: number;
    min?: number;
    max?: number;
    allowed?: readonly number[];
};
export type EnumQueryField<T extends string = string> = {
    kind: 'enum';
    default: T;
    values: readonly T[];
};
export type QueryField = StringQueryField | IntegerQueryField | EnumQueryField;
export type QuerySchema = Record<string, QueryField>;

export type QueryValue<F extends QueryField> = F extends IntegerQueryField ? number : string;
export type QueryValues<S extends QuerySchema> = { [K in keyof S]: QueryValue<S[K]> };

export function stringField(defaultValue = ''): StringQueryField {
    return { kind: 'string', default: defaultValue };
}

export function integerField(
    defaultValue: number,
    options?: Omit<IntegerQueryField, 'kind' | 'default'>
): IntegerQueryField {
    return { kind: 'integer', default: defaultValue, ...options };
}

export function enumField<T extends string>(
    values: readonly T[],
    defaultValue: T
): EnumQueryField<T> {
    return { kind: 'enum', default: defaultValue, values };
}

function parseField(raw: string | null, field: QueryField): string | number {
    if (field.kind === 'string') return raw ?? field.default;
    if (field.kind === 'enum') {
        if (raw !== null && (field.values as readonly string[]).includes(raw)) return raw;
        return field.default;
    }
    if (raw === null || raw === '') return field.default;
    const parsed = Number.parseInt(raw, 10);
    if (!Number.isFinite(parsed)) return field.default;
    if (field.allowed && !field.allowed.includes(parsed)) return field.default;
    if (field.min !== undefined && parsed < field.min) return field.min;
    if (field.max !== undefined && parsed > field.max) return field.max;
    return parsed;
}

export function parseListQuery<S extends QuerySchema>(
    params: URLSearchParams,
    schema: S
): QueryValues<S> {
    const values = {} as QueryValues<S>;
    for (const key of Object.keys(schema) as Array<keyof S>) {
        values[key] = parseField(
            params.get(String(key)),
            schema[key]
        ) as QueryValues<S>[typeof key];
    }
    return values;
}

export function serializeListQuery<S extends QuerySchema>(
    values: QueryValues<S>,
    schema: S,
    current?: URLSearchParams
): URLSearchParams {
    const next = new URLSearchParams(current);
    for (const key of Object.keys(schema)) next.delete(key);
    for (const key of Object.keys(schema) as Array<keyof S>) {
        const field = schema[key];
        const value = values[key];
        if (value === field.default) continue;
        next.set(String(key), String(value));
    }
    return next;
}

export function patchListQuery<S extends QuerySchema>(
    current: URLSearchParams,
    schema: S,
    patch: Partial<QueryValues<S>>,
    options?: { resetPage?: boolean }
): URLSearchParams {
    const values = { ...parseListQuery(current, schema), ...patch };
    if (options?.resetPage && 'page' in schema) {
        (values as QueryValues<S> & { page: number }).page = (
            schema.page as IntegerQueryField
        ).default;
    }
    return serializeListQuery(values, schema, current);
}
