import type { CompletionContext } from '@codemirror/autocomplete';
import type { StreamParser } from '@codemirror/language';
import type { Diagnostic as CodeMirrorDiagnostic } from '@codemirror/lint';
import type { WAFDSLDiagnostic } from '@/api';

import { autocompletion, closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import {
    bracketMatching,
    HighlightStyle,
    indentOnInput,
    StreamLanguage,
    syntaxHighlighting,
} from '@codemirror/language';
import { setDiagnostics } from '@codemirror/lint';
import { EditorState } from '@codemirror/state';
import {
    drawSelection,
    dropCursor,
    EditorView,
    highlightActiveLine,
    highlightActiveLineGutter,
    highlightSpecialChars,
    keymap,
    lineNumbers,
} from '@codemirror/view';
import { tags } from '@lezer/highlight';
import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react';

const keywords = new Set([
    'waf',
    'ruleset',
    'rule',
    'rate_limit',
    'enabled',
    'disabled',
    'trusted_proxy_chain',
    'trusted_proxies',
    'when',
    'and',
    'or',
    'not',
    'exists',
    'eq',
    'contains',
    'starts_with',
    'ends_with',
    'matches',
    'in',
    'case_sensitive',
    'then',
    'status',
    'response',
    'count_by',
    'limit',
    'per',
    'burst',
    'backend',
    'fallback',
]);

const actions = new Set([
    'block',
    'show_page',
    'captcha',
    'redirect',
    'allow',
    'tag',
    'monitor',
    'default',
    'html',
    'text',
    'json',
    'local',
    'redis',
    'open',
    'closed',
]);

const fields = [
    'http.request.method',
    'http.host',
    'http.request.uri.path',
    'http.request.uri.query',
    'http.request.uri.query.values',
    'http.request.headers',
    'http.request.cookies',
    'http.request.target',
    'http.request.body.raw',
    'http.user_agent',
    'ip.src',
    'ip.src.country',
    'ip.src.region_code',
    'global',
];

const wafLanguage = StreamLanguage.define({
    token(stream) {
        if (stream.eatSpace()) return null;
        if (stream.match('//') || stream.match('#')) {
            stream.skipToEnd();
            return 'comment';
        }
        if (stream.peek() === '"') {
            stream.next();
            let escaped = false;
            while (!stream.eol()) {
                const next = stream.next();
                if (next === '"' && !escaped) break;
                escaped = next === '\\' && !escaped;
                if (next !== '\\') escaped = false;
            }
            return 'string';
        }
        if (stream.match(/^\d+(?:\.\d+){3}\/\d+/) || stream.match(/^[0-9A-Fa-f:]+\/\d+/))
            return 'number';
        if (stream.match(/^\d+s\b/) || stream.match(/^\d+\b/)) return 'number';
        if (stream.match(/^[{}()[\]+,]/)) return 'bracket';
        if (stream.match(/^[A-Za-z_][A-Za-z0-9_.-]*/)) {
            const word = stream.current();
            if (keywords.has(word)) return 'keyword';
            if (actions.has(word)) return 'atom';
            if (fields.includes(word)) return 'variableName';
            return 'name';
        }
        stream.next();
        return 'invalid';
    },
} satisfies StreamParser<unknown>);

const completionOptions = [
    ...Array.from(keywords).map((label) => ({ label, type: 'keyword' })),
    ...Array.from(actions).map((label) => ({ label, type: 'keyword' })),
    ...fields.map((label) => ({ label, type: 'variable' })),
];

function complete(context: CompletionContext) {
    const word = context.matchBefore(/[A-Za-z0-9_.-]*/);
    if (!word || (word.from === word.to && !context.explicit)) return null;
    return { from: word.from, options: completionOptions };
}

function diagnosticPosition(state: EditorState, line: number, column: number) {
    const safeLine = Math.max(1, Math.min(line, state.doc.lines));
    const value = state.doc.line(safeLine);
    return Math.max(value.from, Math.min(value.from + Math.max(0, column - 1), value.to));
}

function codeMirrorDiagnostics(state: EditorState, diagnostics: WAFDSLDiagnostic[]) {
    return diagnostics.map<CodeMirrorDiagnostic>((diagnostic) => {
        const from = diagnosticPosition(state, diagnostic.line, diagnostic.column);
        const to = Math.max(
            from + 1,
            diagnosticPosition(state, diagnostic.end_line, diagnostic.end_column)
        );
        return {
            from,
            to: Math.min(to, state.doc.length),
            severity: diagnostic.severity,
            message: diagnostic.message,
        };
    });
}

export interface WAFDSLCodeEditorHandle {
    revealDiagnostic: (diagnostic: WAFDSLDiagnostic) => void;
}

export const WAFDSLCodeEditor = forwardRef<
    WAFDSLCodeEditorHandle,
    {
        value: string;
        diagnostics: WAFDSLDiagnostic[];
        onChange: (value: string) => void;
    }
>(function WAFDSLCodeEditor({ value, diagnostics, onChange }, ref) {
    const hostRef = useRef<HTMLDivElement>(null);
    const viewRef = useRef<EditorView | null>(null);
    const initialValueRef = useRef(value);
    const onChangeRef = useRef(onChange);
    onChangeRef.current = onChange;

    useImperativeHandle(ref, () => ({
        revealDiagnostic(diagnostic) {
            const view = viewRef.current;
            if (!view) return;
            const position = diagnosticPosition(view.state, diagnostic.line, diagnostic.column);
            view.dispatch({ selection: { anchor: position }, scrollIntoView: true });
            view.focus();
        },
    }));

    useEffect(() => {
        if (!hostRef.current) return;
        const state = EditorState.create({
            doc: initialValueRef.current,
            extensions: [
                lineNumbers(),
                highlightActiveLineGutter(),
                highlightSpecialChars(),
                history(),
                drawSelection(),
                dropCursor(),
                indentOnInput(),
                bracketMatching(),
                closeBrackets(),
                autocompletion({ override: [complete] }),
                highlightActiveLine(),
                wafLanguage,
                syntaxHighlighting(
                    HighlightStyle.define([
                        { tag: tags.keyword, color: 'var(--color-primary)', fontWeight: '600' },
                        { tag: tags.string, color: 'var(--color-success)' },
                        { tag: tags.number, color: 'var(--color-warning)' },
                        { tag: tags.variableName, color: 'var(--color-foreground)' },
                        { tag: tags.atom, color: 'var(--color-danger)' },
                        { tag: tags.comment, color: 'var(--color-muted)', fontStyle: 'italic' },
                        { tag: tags.invalid, color: 'var(--color-danger)' },
                    ])
                ),
                keymap.of([
                    ...closeBracketsKeymap,
                    ...defaultKeymap,
                    ...historyKeymap,
                    indentWithTab,
                ]),
                EditorView.updateListener.of((update) => {
                    if (update.docChanged) onChangeRef.current(update.state.doc.toString());
                }),
                EditorView.theme({
                    '&': {
                        height: 'min(62dvh, 680px)',
                        backgroundColor: 'var(--color-surface)',
                        color: 'var(--color-foreground)',
                        fontSize: '13px',
                    },
                    '.cm-scroller': {
                        fontFamily:
                            'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                        lineHeight: '1.65',
                    },
                    '.cm-gutters': {
                        backgroundColor: 'var(--color-surface-secondary)',
                        color: 'var(--color-muted)',
                        borderRight: '1px solid var(--color-border)',
                    },
                    '.cm-activeLine, .cm-activeLineGutter': {
                        backgroundColor: 'color-mix(in srgb, var(--color-primary) 7%, transparent)',
                    },
                    '.cm-content': { padding: '12px 0' },
                    '&.cm-focused': { outline: 'none' },
                    '&.cm-focused .cm-cursor': { borderLeftColor: 'var(--color-foreground)' },
                    '&.cm-focused .cm-selectionBackground, ::selection': {
                        backgroundColor:
                            'color-mix(in srgb, var(--color-primary) 24%, transparent)',
                    },
                }),
            ],
        });
        const view = new EditorView({ state, parent: hostRef.current });
        viewRef.current = view;
        return () => {
            view.destroy();
            viewRef.current = null;
        };
    }, []);

    useEffect(() => {
        const view = viewRef.current;
        if (!view || view.state.doc.toString() === value) return;
        view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } });
    }, [value]);

    useEffect(() => {
        const view = viewRef.current;
        if (view) {
            view.dispatch(
                setDiagnostics(view.state, codeMirrorDiagnostics(view.state, diagnostics))
            );
        }
    }, [diagnostics]);

    return <div ref={hostRef} className='overflow-hidden rounded-lg border border-border' />;
});
