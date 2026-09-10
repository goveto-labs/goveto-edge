import { Fragment, useMemo } from 'react';

type DiffKind = 'add' | 'remove' | 'same';

interface DiffLine {
    key: number;
    kind: DiffKind;
    text: string;
}

type JsonTokenType = 'key' | 'string' | 'number' | 'boolean' | 'null' | 'punct' | 'text';

interface JsonToken {
    key: number;
    text: string;
    type: JsonTokenType;
}

const TOKEN_CLASS: Record<JsonTokenType, string> = {
    key: 'text-accent',
    string: 'text-success',
    number: 'text-warning',
    boolean: 'text-danger',
    null: 'text-muted italic',
    punct: 'text-muted',
    text: '',
};

const JSON_TOKEN_RE =
    /(?<key>"(?:\\.|[^"\\])*")(?=\s*:)|(?<str>"(?:\\.|[^"\\])*")|(?<num>-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|(?<bool>true|false)|(?<null>null)|(?<punct>[{}[\],])/g;

function tokenizeJson(line: string): JsonToken[] {
    JSON_TOKEN_RE.lastIndex = 0;
    const tokens: JsonToken[] = [];
    let last = 0;
    let counter = 0;
    let match = JSON_TOKEN_RE.exec(line);
    while (match !== null) {
        if (match.index > last) {
            tokens.push({ key: counter++, text: line.slice(last, match.index), type: 'text' });
        }
        const groups = match.groups ?? {};
        let type: JsonTokenType = 'text';
        if (groups.key !== undefined) type = 'key';
        else if (groups.str !== undefined) type = 'string';
        else if (groups.num !== undefined) type = 'number';
        else if (groups.bool !== undefined) type = 'boolean';
        else if (groups.null !== undefined) type = 'null';
        else if (groups.punct !== undefined) type = 'punct';
        tokens.push({ key: counter++, text: match[0], type });
        last = match.index + match[0].length;
        match = JSON_TOKEN_RE.exec(line);
    }
    if (last < line.length) {
        tokens.push({ key: counter++, text: line.slice(last), type: 'text' });
    }
    return tokens;
}

function toJsonLines(value: unknown): string[] {
    if (value === undefined) return [];
    return JSON.stringify(value, null, 2).split('\n');
}

function diffLines(oldLines: string[], newLines: string[]): DiffLine[] {
    const n = oldLines.length;
    const m = newLines.length;
    const dp: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
    for (let i = n - 1; i >= 0; i--) {
        for (let j = m - 1; j >= 0; j--) {
            dp[i][j] =
                oldLines[i] === newLines[j]
                    ? dp[i + 1][j + 1] + 1
                    : Math.max(dp[i + 1][j], dp[i][j + 1]);
        }
    }
    const result: DiffLine[] = [];
    let key = 0;
    let i = 0;
    let j = 0;
    while (i < n && j < m) {
        if (oldLines[i] === newLines[j]) {
            result.push({ key: key++, kind: 'same', text: oldLines[i] });
            i++;
            j++;
        } else if (dp[i + 1][j] >= dp[i][j + 1]) {
            result.push({ key: key++, kind: 'remove', text: oldLines[i] });
            i++;
        } else {
            result.push({ key: key++, kind: 'add', text: newLines[j] });
            j++;
        }
    }
    while (i < n) result.push({ key: key++, kind: 'remove', text: oldLines[i++] });
    while (j < m) result.push({ key: key++, kind: 'add', text: newLines[j++] });
    return result;
}

interface SideRow {
    key: number;
    left: DiffLine | null;
    right: DiffLine | null;
}

/**
 * Turn a unified line diff into aligned side-by-side rows. Within each
 * contiguous change hunk the removed and added lines are zipped together so
 * the "before" and "after" versions of a change sit on the same row; surplus
 * lines get a null counterpart on the opposite side (rendered as an empty
 * placeholder) so the two columns stay vertically aligned.
 */
function toSideBySide(lines: DiffLine[]): SideRow[] {
    const rows: SideRow[] = [];
    let buffer: DiffLine[] = [];
    let rowKey = 0;
    const flush = () => {
        if (buffer.length === 0) return;
        const removes = buffer.filter((line) => line.kind === 'remove');
        const adds = buffer.filter((line) => line.kind === 'add');
        const max = Math.max(removes.length, adds.length);
        for (let k = 0; k < max; k++) {
            rows.push({ key: rowKey++, left: removes[k] ?? null, right: adds[k] ?? null });
        }
        buffer = [];
    };
    for (const line of lines) {
        if (line.kind === 'same') {
            flush();
            rows.push({ key: rowKey++, left: line, right: line });
        } else {
            buffer.push(line);
        }
    }
    flush();
    return rows;
}

function DiffCell({ line, side }: { line: DiffLine | null; side: 'left' | 'right' }) {
    if (!line) {
        return <div className='px-2 leading-6'>&nbsp;</div>;
    }
    const tokens = tokenizeJson(line.text);
    const removed = line.kind === 'remove';
    const added = line.kind === 'add';
    const background = removed ? 'bg-danger/10' : added ? 'bg-success/10' : '';
    const marker = side === 'left' ? (removed ? '-' : '') : added ? '+' : '';
    const markerClass =
        side === 'left' ? (removed ? 'text-danger' : '') : added ? 'text-success' : '';
    return (
        <div
            className={`flex items-start gap-1 overflow-x-auto whitespace-pre border-border px-2 leading-6 [scrollbar-width:thin] ${
                side === 'left' ? 'border-r' : ''
            } ${background}`}
        >
            <span className={`w-3 shrink-0 select-none text-center ${markerClass}`}>{marker}</span>
            <code className='font-mono'>
                {tokens.length === 0
                    ? '\u00a0'
                    : tokens.map((token) => (
                          <span key={token.key} className={TOKEN_CLASS[token.type]}>
                              {token.text}
                          </span>
                      ))}
            </code>
        </div>
    );
}

interface JsonDiffProps {
    before?: unknown;
    after?: unknown;
}

export function JsonDiff({ before, after }: JsonDiffProps) {
    const rows = useMemo(
        () => toSideBySide(diffLines(toJsonLines(before), toJsonLines(after))),
        [before, after]
    );
    const added = rows.filter((row) => row.right?.kind === 'add').length;
    const removed = rows.filter((row) => row.left?.kind === 'remove').length;
    const empty = rows.length === 0;

    return (
        <div className='overflow-hidden rounded-lg border border-border bg-surface-secondary text-xs'>
            <div className='flex items-center justify-between gap-2 border-b border-border px-3 py-2'>
                <span className='font-semibold'>Changes</span>
                <span className='flex items-center gap-3 font-mono tabular text-muted'>
                    <span className='text-success'>+{added}</span>
                    <span className='text-danger'>-{removed}</span>
                </span>
            </div>
            <div className='grid grid-cols-2 border-b border-border'>
                <div className='border-r border-border px-3 py-1.5 font-medium text-muted'>
                    Before
                </div>
                <div className='px-3 py-1.5 font-medium text-muted'>After</div>
            </div>
            <div className='max-h-[58vh] overflow-y-auto'>
                {empty ? (
                    <div className='px-3 py-4 text-muted'>No snapshot data.</div>
                ) : (
                    <div className='grid grid-cols-2'>
                        {rows.map((row) => (
                            <Fragment key={row.key}>
                                <DiffCell line={row.left} side='left' />
                                <DiffCell line={row.right} side='right' />
                            </Fragment>
                        ))}
                    </div>
                )}
            </div>
        </div>
    );
}
