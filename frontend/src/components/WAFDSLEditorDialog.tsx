import type {
    SecurityPolicy,
    WAFDSLDiagnostic,
    WAFDSLRenderResponse,
    WAFDSLRequest,
    WAFDSLValidationResponse,
} from '@/api';
import type { WAFDSLCodeEditorHandle } from '@/components/WAFDSLCodeEditor.tsx';

import { Button } from '@heroui/react';
import { AlertCircle, Braces, CheckCircle2, LoaderCircle, WandSparkles } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';

import { ApiError } from '@/api';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { WAFDSLCodeEditor } from '@/components/WAFDSLCodeEditor.tsx';

export interface WAFDSLEditRequest {
    title: string;
    subtitle: string;
    request: WAFDSLRequest;
    onApply: (waf: SecurityPolicy['waf']) => void;
}

export function WAFDSLEditorDialog({
    value,
    renderDSL,
    validateDSL,
    onClose,
}: {
    value: WAFDSLEditRequest | null;
    renderDSL: (request: WAFDSLRequest) => Promise<WAFDSLRenderResponse>;
    validateDSL: (request: WAFDSLRequest) => Promise<WAFDSLValidationResponse>;
    onClose: () => void;
}) {
    const editorRef = useRef<WAFDSLCodeEditorHandle>(null);
    const requestVersion = useRef(0);
    const [source, setSource] = useState('');
    const [validatedSource, setValidatedSource] = useState('');
    const [validation, setValidation] = useState<WAFDSLValidationResponse | null>(null);
    const [loading, setLoading] = useState(false);
    const [validating, setValidating] = useState(false);
    const [error, setError] = useState('');

    useEffect(() => {
        if (!value) return;
        const version = ++requestVersion.current;
        setLoading(true);
        setError('');
        setSource('');
        setValidatedSource('');
        setValidation(null);
        void renderDSL(value.request)
            .then((response) => {
                if (version !== requestVersion.current) return;
                setSource(response.source);
            })
            .catch((loadError) => {
                if (version !== requestVersion.current) return;
                setError(
                    loadError instanceof ApiError ? loadError.message : 'Failed to render DSL'
                );
            })
            .finally(() => {
                if (version === requestVersion.current) setLoading(false);
            });
        return () => {
            requestVersion.current++;
        };
    }, [renderDSL, value]);

    const validate = useCallback(
        async (candidate: string) => {
            if (!value) return null;
            const version = ++requestVersion.current;
            setValidating(true);
            setError('');
            try {
                const result = await validateDSL({ ...value.request, source: candidate });
                if (version !== requestVersion.current) return null;
                setValidatedSource(candidate);
                setValidation(result);
                return result;
            } catch (validateError) {
                if (version !== requestVersion.current) return null;
                setValidation(null);
                setError(
                    validateError instanceof ApiError
                        ? validateError.message
                        : 'Failed to validate DSL'
                );
                return null;
            } finally {
                if (version === requestVersion.current) setValidating(false);
            }
        },
        [validateDSL, value]
    );

    useEffect(() => {
        if (!value || loading || !source) return;
        const timer = window.setTimeout(() => void validate(source), 300);
        return () => window.clearTimeout(timer);
    }, [loading, source, validate, value]);

    const apply = async () => {
        if (!value) return;
        const result = validatedSource === source ? validation : await validate(source);
        if (!result?.valid || !result.policy) return;
        value.onApply(result.policy);
        onClose();
    };

    const format = async () => {
        const result = await validate(source);
        if (result?.valid) setSource(result.source);
    };

    const diagnostics = validation?.diagnostics ?? [];
    const isCurrent = validatedSource === source;
    const canApply = isCurrent && Boolean(validation?.valid && validation.policy) && !validating;

    return (
        <DialogShell
            clusterContext='none'
            icon={<Braces className='h-5 w-5' />}
            isDismissable={!validating}
            isOpen={Boolean(value)}
            size='xl'
            subtitle={value?.subtitle}
            title={value?.title ?? 'Edit WAF DSL'}
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
        >
            <div className='space-y-3 px-4 py-4 sm:px-6'>
                <div className='flex min-h-8 flex-wrap items-center justify-between gap-2'>
                    <div aria-live='polite' className='flex items-center gap-2 text-xs'>
                        {loading || validating ? (
                            <>
                                <LoaderCircle
                                    aria-hidden='true'
                                    className='h-4 w-4 animate-spin text-muted'
                                />
                                <span className='text-muted'>
                                    {loading ? 'Loading DSL…' : 'Checking syntax…'}
                                </span>
                            </>
                        ) : isCurrent && validation?.valid ? (
                            <>
                                <CheckCircle2 aria-hidden='true' className='h-4 w-4 text-success' />
                                <span className='font-medium text-success'>Valid WAF policy</span>
                            </>
                        ) : diagnostics.length > 0 ? (
                            <>
                                <AlertCircle aria-hidden='true' className='h-4 w-4 text-danger' />
                                <span className='font-medium tabular text-danger'>
                                    {diagnostics.length} issue{diagnostics.length === 1 ? '' : 's'}
                                </span>
                            </>
                        ) : null}
                    </div>
                    <Button
                        isDisabled={loading || validating || !source}
                        size='sm'
                        variant='secondary'
                        onPress={() => void format()}
                    >
                        <WandSparkles aria-hidden='true' className='h-4 w-4' />
                        Format
                    </Button>
                </div>
                {loading ? (
                    <div className='flex h-[min(62dvh,680px)] items-center justify-center rounded-lg border border-border bg-surface-secondary/35'>
                        <LoaderCircle
                            aria-hidden='true'
                            className='h-5 w-5 animate-spin text-muted'
                        />
                    </div>
                ) : (
                    <WAFDSLCodeEditor
                        ref={editorRef}
                        diagnostics={isCurrent ? diagnostics : []}
                        value={source}
                        onChange={setSource}
                    />
                )}
                {error && (
                    <div className='rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger'>
                        {error}
                    </div>
                )}
                {isCurrent && diagnostics.length > 0 && (
                    <div className='max-h-28 overflow-y-auto rounded-lg border border-border bg-surface-secondary/35 p-1'>
                        {diagnostics.map((diagnostic: WAFDSLDiagnostic) => (
                            <button
                                key={`${diagnostic.line}:${diagnostic.column}:${diagnostic.end_line}:${diagnostic.end_column}:${diagnostic.message}`}
                                className='flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-left text-xs hover:bg-surface'
                                type='button'
                                onClick={() => editorRef.current?.revealDiagnostic(diagnostic)}
                            >
                                <span className='shrink-0 font-mono tabular text-danger'>
                                    {diagnostic.line}:{diagnostic.column}
                                </span>
                                <span className='text-foreground'>{diagnostic.message}</span>
                            </button>
                        ))}
                    </div>
                )}
            </div>
            <DialogFooter>
                <Button variant='ghost' onPress={onClose}>
                    Cancel
                </Button>
                <Button isDisabled={!canApply} variant='primary' onPress={() => void apply()}>
                    Apply DSL
                </Button>
            </DialogFooter>
        </DialogShell>
    );
}
