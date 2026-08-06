import { Button, Input, Tabs, TextArea } from '@heroui/react';
import { Globe2, Plus, Sparkles, X } from 'lucide-react';
import { useMemo, useState } from 'react';

import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';

type AddMode = 'single' | 'multiple';

function normalizeDomain(value: string) {
    return value.trim().toLowerCase().replace(/\.$/, '');
}

function validateDomain(value: string, allowWildcard: boolean) {
    if (!value) return 'Enter a domain.';
    if (value.includes('://') || /[/:,\s]/.test(value)) {
        return `Enter a hostname without a protocol, port, or path: ${value}`;
    }
    const wildcard = value.startsWith('*.');
    if (wildcard && !allowWildcard) {
        return `Wildcard domains are not allowed: ${value}`;
    }
    const hostname = wildcard ? value.slice(2) : value;
    if (!hostname.includes('.') || hostname.startsWith('.') || hostname.endsWith('.')) {
        return `Enter a valid hostname: ${value}`;
    }
    if (hostname.includes('*')) {
        return `Only a leading *. wildcard label is supported: ${value}`;
    }
    return '';
}

function apexFromDomain(domain: string) {
    return domain.startsWith('*.') ? domain.slice(2) : domain;
}

export function DomainAddField({
    value,
    onChange,
    allowWildcard = false,
    addLabel = 'Add domain',
    emptyLabel = 'No domains added',
    hint,
}: {
    value: string[];
    onChange: (domains: string[]) => void;
    allowWildcard?: boolean;
    addLabel?: string;
    emptyLabel?: string;
    hint?: string;
}) {
    const [open, setOpen] = useState(false);
    const [mode, setMode] = useState<AddMode>('single');
    const [singleDomain, setSingleDomain] = useState('');
    const [multipleDomains, setMultipleDomains] = useState('');
    const [includeWildcard, setIncludeWildcard] = useState(false);
    const [error, setError] = useState('');

    const candidates = useMemo(() => {
        const source = mode === 'single' ? [singleDomain] : multipleDomains.split(/\r?\n/);
        const seen = new Set<string>();
        const result: string[] = [];
        for (const raw of source) {
            const domain = normalizeDomain(raw);
            if (!domain || seen.has(domain)) continue;
            seen.add(domain);
            result.push(domain);
            if (allowWildcard && includeWildcard && mode === 'single' && !domain.startsWith('*.')) {
                const wildcard = `*.${domain}`;
                if (!seen.has(wildcard)) {
                    seen.add(wildcard);
                    result.push(wildcard);
                }
            }
        }
        return result;
    }, [allowWildcard, includeWildcard, mode, multipleDomains, singleDomain]);

    const handleOpenChange = (nextOpen: boolean) => {
        setOpen(nextOpen);
        if (nextOpen) {
            setMode('single');
            setSingleDomain('');
            setMultipleDomains('');
            setIncludeWildcard(false);
            setError('');
        }
    };

    const addDomains = () => {
        if (candidates.length === 0) {
            setError(mode === 'single' ? 'Enter a domain.' : 'Enter at least one domain.');
            return;
        }
        const invalid = candidates.find((domain) => validateDomain(domain, allowWildcard));
        if (invalid) {
            setError(validateDomain(invalid, allowWildcard));
            return;
        }

        const existing = new Set(value.map(normalizeDomain));
        const additions = candidates.filter((domain) => !existing.has(domain));
        if (additions.length === 0) {
            setError(
                candidates.length === 1
                    ? 'This domain is already added.'
                    : 'All domains are already added.'
            );
            return;
        }

        onChange([...value, ...additions]);
        setOpen(false);
    };

    const addWildcardPair = (domain: string) => {
        const apex = apexFromDomain(domain);
        const next = new Set(value.map(normalizeDomain));
        next.add(apex);
        next.add(`*.${apex}`);
        onChange(Array.from(next));
    };

    return (
        <div className='space-y-3'>
            {hint && <p className='text-xs text-muted'>{hint}</p>}
            <div className='flex min-h-8 flex-wrap items-center gap-2'>
                {value.length === 0 && <span className='text-sm text-muted'>{emptyLabel}</span>}
                {value.map((domain) => (
                    <span
                        className='inline-flex max-w-full items-center gap-1.5 rounded-full border border-border bg-surface-secondary px-3 py-1 text-sm'
                        key={domain}
                    >
                        <span className='truncate font-mono text-xs'>{domain}</span>
                        {allowWildcard &&
                            !domain.startsWith('*.') &&
                            !value.includes(`*.${domain}`) && (
                                <button
                                    aria-label={`Add wildcard for ${domain}`}
                                    className='shrink-0 rounded-full text-muted transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary'
                                    title='Add matching wildcard'
                                    type='button'
                                    onClick={() => addWildcardPair(domain)}
                                >
                                    <Sparkles className='h-3.5 w-3.5' />
                                </button>
                            )}
                        <button
                            aria-label={`Remove ${domain}`}
                            className='shrink-0 rounded-full text-muted transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary'
                            type='button'
                            onClick={() => onChange(value.filter((item) => item !== domain))}
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
                icon={<Globe2 className='h-5 w-5' />}
                isOpen={open}
                size='md'
                subtitle={
                    allowWildcard
                        ? 'Add hostnames, paste a list, or include a matching wildcard.'
                        : 'Add one hostname or paste a list of hostnames.'
                }
                title='Add domains'
                onOpenChange={handleOpenChange}
            >
                <div className='px-6 py-4'>
                    <Tabs
                        aria-label='Domain input mode'
                        selectedKey={mode}
                        onSelectionChange={(key) => {
                            setMode(key as AddMode);
                            setError('');
                        }}
                    >
                        <Tabs.List className='w-full'>
                            <Tabs.Tab className='flex-1' id='single'>
                                Single domain
                            </Tabs.Tab>
                            <Tabs.Tab className='flex-1' id='multiple'>
                                Multiple domains
                            </Tabs.Tab>
                        </Tabs.List>

                        <Tabs.Panel className='pt-2' id='single'>
                            <FormField
                                hint={
                                    allowWildcard
                                        ? 'Examples: example.com or *.example.com'
                                        : 'Enter a hostname only, for example example.com.'
                                }
                                htmlFor='single-site-domain'
                                label='Domain'
                                required
                            >
                                <Input
                                    autoFocus
                                    id='single-site-domain'
                                    placeholder={
                                        allowWildcard
                                            ? 'example.com or *.example.com'
                                            : 'example.com'
                                    }
                                    value={singleDomain}
                                    variant='secondary'
                                    onChange={(event) => {
                                        setSingleDomain(event.target.value);
                                        setError('');
                                    }}
                                    onKeyDown={(event) => {
                                        if (event.key === 'Enter') {
                                            event.preventDefault();
                                            addDomains();
                                        }
                                    }}
                                />
                            </FormField>
                            {allowWildcard && (
                                <label className='mt-3 flex items-center gap-2 text-sm'>
                                    <input
                                        checked={includeWildcard}
                                        type='checkbox'
                                        onChange={(event) =>
                                            setIncludeWildcard(event.target.checked)
                                        }
                                    />
                                    Also add matching wildcard (*.domain)
                                </label>
                            )}
                        </Tabs.Panel>

                        <Tabs.Panel className='pt-2' id='multiple'>
                            <FormField
                                hint={
                                    allowWildcard
                                        ? 'One hostname per line. Wildcards like *.example.com are allowed.'
                                        : 'Empty lines and duplicate entries are ignored.'
                                }
                                htmlFor='multiple-site-domains'
                                label='Domains, one per line'
                                required
                            >
                                <TextArea
                                    autoFocus
                                    id='multiple-site-domains'
                                    placeholder={
                                        allowWildcard
                                            ? 'example.com\n*.example.com\nwww.example.com'
                                            : 'example.com\nwww.example.com\nassets.example.com'
                                    }
                                    rows={8}
                                    value={multipleDomains}
                                    variant='secondary'
                                    onChange={(event) => {
                                        setMultipleDomains(event.target.value);
                                        setError('');
                                    }}
                                />
                            </FormField>
                        </Tabs.Panel>
                    </Tabs>

                    {error && <FormError message={error} />}
                </div>
                <DialogFooter>
                    <div className='mr-auto self-center text-sm text-muted'>
                        {candidates.length > 0
                            ? `${candidates.length} ${candidates.length === 1 ? 'domain' : 'domains'} ready`
                            : 'No domains ready'}
                    </div>
                    <Button type='button' variant='ghost' onPress={() => setOpen(false)}>
                        Cancel
                    </Button>
                    <Button type='button' onPress={addDomains}>
                        Add {mode === 'single' ? 'domain' : 'domains'}
                    </Button>
                </DialogFooter>
            </DialogShell>
        </div>
    );
}
