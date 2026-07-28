import { Button, Input } from '@heroui/react';
import { Plus, X } from 'lucide-react';
import { useState } from 'react';

import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';

export function ValueListAddField({
    label,
    values,
    addLabel,
    dialogTitle,
    placeholder,
    hint,
    emptyLabel,
    normalize = (value) => value.trim(),
    validate,
    onChange,
}: {
    label: string;
    values: string[];
    addLabel: string;
    dialogTitle: string;
    placeholder?: string;
    hint?: string;
    emptyLabel: string;
    normalize?: (value: string) => string;
    validate?: (value: string) => string;
    onChange: (values: string[]) => void;
}) {
    const [open, setOpen] = useState(false);
    const [draft, setDraft] = useState('');
    const [error, setError] = useState('');
    const inputId = `add-${label.toLocaleLowerCase().replace(/\s+/g, '-')}`;

    const handleOpenChange = (nextOpen: boolean) => {
        setOpen(nextOpen);
        if (nextOpen) {
            setDraft('');
            setError('');
        }
    };

    const addValue = () => {
        const value = normalize(draft);
        if (!value) {
            setError(`Enter a ${label.toLocaleLowerCase()}.`);
            return;
        }
        const validationError = validate?.(value) ?? '';
        if (validationError) {
            setError(validationError);
            return;
        }
        if (values.includes(value)) {
            setError(`${value} is already added.`);
            return;
        }
        onChange([...values, value]);
        setOpen(false);
    };

    return (
        <div className='space-y-1.5'>
            <div className='text-sm font-medium'>{label}</div>
            <div className='flex min-h-10 flex-wrap items-center gap-2'>
                {values.length === 0 && <span className='text-sm text-muted'>{emptyLabel}</span>}
                {values.map((value) => (
                    <span
                        className='inline-flex max-w-full items-center gap-1.5 rounded-full border border-border bg-surface-secondary px-3 py-1 text-sm'
                        key={value}
                    >
                        <span className='truncate font-mono text-xs'>{value}</span>
                        <button
                            aria-label={`Remove ${value}`}
                            className='shrink-0 rounded-full text-muted transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary'
                            type='button'
                            onClick={() => onChange(values.filter((item) => item !== value))}
                        >
                            <X className='h-3.5 w-3.5' />
                        </button>
                    </span>
                ))}
                <Button
                    size='sm'
                    type='button'
                    variant='secondary'
                    onPress={() => handleOpenChange(true)}
                >
                    <Plus className='mr-1.5 h-4 w-4' />
                    {addLabel}
                </Button>
            </div>

            <DialogShell
                isOpen={open}
                size='sm'
                title={dialogTitle}
                onOpenChange={handleOpenChange}
            >
                <div className='space-y-3 px-6 py-5'>
                    <FormField htmlFor={inputId} label={label} hint={hint} required>
                        <Input
                            autoFocus
                            id={inputId}
                            placeholder={placeholder}
                            value={draft}
                            variant='secondary'
                            onChange={(event) => {
                                setDraft(event.target.value);
                                setError('');
                            }}
                            onKeyDown={(event) => {
                                if (event.key === 'Enter') {
                                    event.preventDefault();
                                    addValue();
                                }
                            }}
                        />
                    </FormField>
                    {error && <FormError message={error} />}
                </div>
                <DialogFooter>
                    <Button type='button' variant='ghost' onPress={() => setOpen(false)}>
                        Cancel
                    </Button>
                    <Button type='button' onPress={addValue}>
                        Add
                    </Button>
                </DialogFooter>
            </DialogShell>
        </div>
    );
}
