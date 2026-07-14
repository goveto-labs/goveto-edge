import { Button, Input } from '@heroui/react';
import { Check, Plus, Search, X } from 'lucide-react';
import { useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';

import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';

export interface MultiAddOption {
    id: string;
    name: string;
    detail?: string;
}

export function SearchableMultiAddField({
    options,
    selected,
    addLabel,
    dialogTitle,
    itemLabel,
    searchPlaceholder,
    createOption,
    emptyLabel,
    onChange,
}: {
    options: MultiAddOption[];
    selected: Set<string>;
    addLabel: string;
    dialogTitle: string;
    itemLabel: string;
    searchPlaceholder: string;
    createOption?: (name: string) => Promise<MultiAddOption>;
    emptyLabel?: string;
    onChange: (value: Set<string>) => void;
}) {
    const rowHeight = 56;
    const viewportHeight = 336;
    const overscan = 5;
    const [open, setOpen] = useState(false);
    const [draft, setDraft] = useState<Set<string>>(new Set());
    const [query, setQuery] = useState('');
    const [selectedOnly, setSelectedOnly] = useState(false);
    const [scrollTop, setScrollTop] = useState(0);
    const [newOptionName, setNewOptionName] = useState('');
    const [creating, setCreating] = useState(false);
    const [createError, setCreateError] = useState('');
    const deferredQuery = useDeferredValue(query.trim().toLocaleLowerCase());
    const scrollRef = useRef<HTMLDivElement>(null);
    const scrollRafRef = useRef<number | undefined>(undefined);

    const optionsById = useMemo(
        () => new Map(options.map((option) => [option.id, option])),
        [options]
    );
    const selectedOptions = useMemo(
        () =>
            Array.from(selected, (id) => optionsById.get(id)).filter(
                (option): option is MultiAddOption => Boolean(option)
            ),
        [optionsById, selected]
    );
    const filteredOptions = useMemo(() => {
        return options.filter((option) => {
            if (selectedOnly && !draft.has(option.id)) return false;
            if (!deferredQuery) return true;
            return `${option.name} ${option.detail ?? ''}`
                .toLocaleLowerCase()
                .includes(deferredQuery);
        });
    }, [deferredQuery, draft, options, selectedOnly]);

    const startIndex = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
    const endIndex = Math.min(
        filteredOptions.length,
        Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan
    );
    const visibleOptions = filteredOptions.slice(startIndex, endIndex);

    const resetScroll = () => {
        setScrollTop(0);
        if (scrollRef.current) scrollRef.current.scrollTop = 0;
    };

    const handleOpenChange = (nextOpen: boolean) => {
        setOpen(nextOpen);
        if (nextOpen) {
            setDraft(new Set(selected));
            setQuery('');
            setSelectedOnly(false);
            setNewOptionName('');
            setCreateError('');
            resetScroll();
        }
    };

    const toggleDraft = (id: string) => {
        setDraft((current) => {
            const next = new Set(current);
            if (next.has(id)) next.delete(id);
            else next.add(id);
            return next;
        });
    };

    const handleCreate = async () => {
        const name = newOptionName.trim();
        if (!name || !createOption || creating) return;

        const existing = options.find(
            (option) => option.name.toLocaleLowerCase() === name.toLocaleLowerCase()
        );
        if (existing) {
            setDraft((current) => new Set(current).add(existing.id));
            setNewOptionName('');
            setCreateError('');
            return;
        }

        setCreating(true);
        setCreateError('');
        try {
            const created = await createOption(name);
            setDraft((current) => new Set(current).add(created.id));
            setNewOptionName('');
            setQuery('');
            setSelectedOnly(false);
            resetScroll();
        } catch (error) {
            setCreateError(
                error instanceof Error ? error.message : `Failed to create ${itemLabel}`
            );
        } finally {
            setCreating(false);
        }
    };

    useEffect(
        () => () => {
            cancelAnimationFrame(scrollRafRef.current ?? 0);
        },
        []
    );

    const displayedSelections = selectedOptions.slice(0, 6);
    const hiddenSelectionCount = selectedOptions.length - displayedSelections.length;

    return (
        <div className='space-y-3'>
            <div className='flex min-h-8 flex-wrap items-center gap-2'>
                {selectedOptions.length === 0 && emptyLabel && (
                    <span className='text-sm text-muted'>{emptyLabel}</span>
                )}
                {displayedSelections.map((option) => (
                    <span
                        className='inline-flex items-center gap-1.5 rounded-full border border-border bg-surface-secondary px-3 py-1 text-sm'
                        key={option.id}
                    >
                        {option.name}
                        <button
                            aria-label={`Remove ${option.name}`}
                            className='rounded-full text-muted transition-colors hover:text-foreground'
                            type='button'
                            onClick={() => {
                                const next = new Set(selected);
                                next.delete(option.id);
                                onChange(next);
                            }}
                        >
                            <X className='h-3.5 w-3.5' />
                        </button>
                    </span>
                ))}
                {hiddenSelectionCount > 0 && (
                    <span className='rounded-full bg-surface-secondary px-3 py-1 text-sm text-muted'>
                        +{hiddenSelectionCount} more
                    </span>
                )}
                <Button
                    size='sm'
                    type='button'
                    variant='secondary'
                    onPress={() => handleOpenChange(true)}
                >
                    <Plus className='mr-1.5 h-4 w-4' />
                    {addLabel}
                    {selected.size > 0 && (
                        <span className='ml-1 rounded-full bg-primary/10 px-1.5 text-xs text-primary'>
                            {selected.size}
                        </span>
                    )}
                </Button>
            </div>

            <DialogShell
                isOpen={open}
                size='lg'
                subtitle={`${options.length} available. Search and select the ${itemLabel}s assigned to this node.`}
                title={dialogTitle}
                onOpenChange={handleOpenChange}
            >
                <div className='space-y-4 px-6 py-5'>
                    {createOption && (
                        <div className='rounded-xl border border-border bg-surface-secondary/30 p-3'>
                            <div className='mb-2 text-sm font-medium'>Create {itemLabel}</div>
                            <div className='flex gap-2'>
                                <Input
                                    aria-label={`New ${itemLabel} name`}
                                    placeholder={`${itemLabel} name`}
                                    value={newOptionName}
                                    variant='secondary'
                                    onChange={(event) => {
                                        setNewOptionName(event.target.value);
                                        setCreateError('');
                                    }}
                                    onKeyDown={(event) => {
                                        if (event.key === 'Enter') {
                                            event.preventDefault();
                                            void handleCreate();
                                        }
                                    }}
                                />
                                <Button
                                    isDisabled={!newOptionName.trim() || creating}
                                    type='button'
                                    onPress={() => void handleCreate()}
                                >
                                    <Plus className='mr-1.5 h-4 w-4' />
                                    {creating ? 'Creating…' : 'Create'}
                                </Button>
                            </div>
                            {createError && (
                                <p className='mt-2 text-sm text-danger'>{createError}</p>
                            )}
                        </div>
                    )}
                    <div className='flex flex-col gap-2 sm:flex-row'>
                        <div className='relative min-w-0 flex-1'>
                            <Search className='pointer-events-none absolute left-3 top-1/2 z-10 h-4 w-4 -translate-y-1/2 text-muted' />
                            <Input
                                autoFocus
                                aria-label={`Search ${itemLabel}s`}
                                className='pl-9'
                                placeholder={searchPlaceholder}
                                value={query}
                                variant='secondary'
                                onChange={(event) => {
                                    setQuery(event.target.value);
                                    resetScroll();
                                }}
                            />
                        </div>
                        <Button
                            type='button'
                            variant={selectedOnly ? 'primary' : 'secondary'}
                            onPress={() => {
                                setSelectedOnly((value) => !value);
                                resetScroll();
                            }}
                        >
                            Selected only ({draft.size})
                        </Button>
                    </div>

                    <div className='flex flex-wrap items-center justify-between gap-2 text-sm'>
                        <span className='text-muted'>
                            {filteredOptions.length} result{filteredOptions.length === 1 ? '' : 's'}
                        </span>
                        <div className='flex gap-2'>
                            <Button
                                isDisabled={filteredOptions.length === 0}
                                size='sm'
                                type='button'
                                variant='ghost'
                                onPress={() =>
                                    setDraft((current) => {
                                        const next = new Set(current);
                                        for (const option of filteredOptions) next.add(option.id);
                                        return next;
                                    })
                                }
                            >
                                Select results
                            </Button>
                            <Button
                                isDisabled={draft.size === 0}
                                size='sm'
                                type='button'
                                variant='ghost'
                                onPress={() => setDraft(new Set())}
                            >
                                Clear
                            </Button>
                        </div>
                    </div>

                    <div
                        ref={scrollRef}
                        className='overflow-y-auto rounded-xl border border-border bg-surface-secondary/20'
                        style={{ height: viewportHeight }}
                        onScroll={(event) => {
                            const nextScrollTop = event.currentTarget.scrollTop;
                            cancelAnimationFrame(scrollRafRef.current ?? 0);
                            scrollRafRef.current = requestAnimationFrame(() => {
                                setScrollTop(nextScrollTop);
                            });
                        }}
                    >
                        {filteredOptions.length === 0 ? (
                            <div className='flex h-full items-center justify-center px-6 text-center text-sm text-muted'>
                                {options.length === 0
                                    ? `No ${itemLabel}s are configured.`
                                    : `No ${itemLabel}s match the current filter.`}
                            </div>
                        ) : (
                            <div
                                className='relative'
                                style={{ height: filteredOptions.length * rowHeight }}
                            >
                                {visibleOptions.map((option, visibleIndex) => {
                                    const index = startIndex + visibleIndex;
                                    const active = draft.has(option.id);
                                    return (
                                        <button
                                            aria-pressed={active}
                                            className={`absolute inset-x-0 flex items-center gap-3 border-b border-border px-4 text-left transition-colors hover:bg-surface-secondary ${
                                                active ? 'bg-accent/10' : 'bg-surface'
                                            }`}
                                            key={option.id}
                                            style={{
                                                height: rowHeight,
                                                transform: `translateY(${index * rowHeight}px)`,
                                            }}
                                            type='button'
                                            onClick={() => toggleDraft(option.id)}
                                        >
                                            <span
                                                className={`flex h-5 w-5 shrink-0 items-center justify-center rounded border ${
                                                    active
                                                        ? 'border-primary bg-primary text-primary-foreground'
                                                        : 'border-border bg-surface'
                                                }`}
                                            >
                                                {active && <Check className='h-3.5 w-3.5' />}
                                            </span>
                                            <span className='min-w-0 flex-1'>
                                                <span className='block truncate text-sm font-medium'>
                                                    {option.name}
                                                </span>
                                                {option.detail && (
                                                    <span className='block truncate text-xs text-muted'>
                                                        {option.detail}
                                                    </span>
                                                )}
                                            </span>
                                            {active && (
                                                <span className='text-xs font-medium text-primary'>
                                                    Selected
                                                </span>
                                            )}
                                        </button>
                                    );
                                })}
                            </div>
                        )}
                    </div>
                </div>
                <DialogFooter>
                    <div className='mr-auto self-center text-sm text-muted'>
                        {draft.size} selected
                    </div>
                    <Button type='button' variant='ghost' onPress={() => setOpen(false)}>
                        Cancel
                    </Button>
                    <Button
                        type='button'
                        onPress={() => {
                            onChange(new Set(draft));
                            setOpen(false);
                        }}
                    >
                        Apply selection
                    </Button>
                </DialogFooter>
            </DialogShell>
        </div>
    );
}
