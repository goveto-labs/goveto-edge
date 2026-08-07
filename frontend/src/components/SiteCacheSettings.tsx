import type { CachePolicy } from '@/api';

import { Button, Checkbox, CheckboxGroup, Input } from '@heroui/react';
import {
    Braces,
    Edit3,
    type HardDrive,
    Hash,
    KeyRound,
    LockKeyhole,
    Plus,
    Save,
    ShieldCheck,
    Trash2,
    X,
} from 'lucide-react';
import { useState } from 'react';

import { ByteSizeInput } from '@/components/ByteSizeInput.tsx';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { DurationInput } from '@/components/DurationInput.tsx';
import { FormField } from '@/components/FormField.tsx';
import { SettingsActionBar } from '@/components/SettingsActionBar.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

type CacheKeyPart = NonNullable<NonNullable<CachePolicy['cache_key']>['parts']>[number];

interface SiteCacheSettingsProps {
    cache: CachePolicy;
    isDirty: boolean;
    saving: boolean;
    onChange: (cache: CachePolicy) => void;
    onDiscard: () => void;
    onSave: () => void;
}

const maxSeconds = 31_536_000;
const maxBodyLimit = 4_294_967_296;
const cacheMethods = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'];
const cacheKeyPartOrder: CacheKeyPart[] = ['METHOD', 'SCHEME', 'HOST', 'PATH', 'QUERY'];
const cacheKeyPartDetails: Record<
    CacheKeyPart,
    { label: string; token: string; description: string }
> = {
    METHOD: {
        label: 'Request method',
        token: '{method}',
        description: 'Separates GET, HEAD, and other cacheable methods.',
    },
    SCHEME: {
        label: 'Scheme',
        token: '{scheme}',
        description: 'Separates HTTP and HTTPS requests.',
    },
    HOST: {
        label: 'Hostname',
        token: '{host}',
        description: 'Separates requests sent to different hostnames.',
    },
    PATH: {
        label: 'URI path',
        token: '{path}',
        description: 'Identifies the requested resource and is always required.',
    },
    QUERY: {
        label: 'Query parameters',
        token: '{query}',
        description: 'Separates requests by their complete query string.',
    },
};
const bypassPresets = [
    { value: 'no-store', description: 'Never cache requests or responses marked no-store.' },
    { value: 'private', description: 'Bypass responses intended for a private cache.' },
    { value: 'no-cache', description: 'Require origin validation instead of using the cache.' },
    { value: 'max-age=0', description: 'Bypass requests that explicitly require fresh content.' },
];

function canonicalHeaderName(value: string) {
    return value
        .trim()
        .toLowerCase()
        .split('-')
        .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
        .join('-');
}

export function Section({
    icon: Icon,
    title,
    description,
    children,
}: {
    icon: typeof HardDrive;
    title: string;
    description: string;
    children: React.ReactNode;
}) {
    return (
        <section className='grid gap-5 border-t border-border px-5 py-6 lg:grid-cols-[220px_minmax(0,1fr)] lg:px-6'>
            <div>
                <div className='flex items-center gap-2.5'>
                    <span className='flex h-8 w-8 items-center justify-center rounded-lg bg-surface-secondary text-muted'>
                        <Icon className='h-4 w-4' />
                    </span>
                    <h3 className='text-sm font-semibold'>{title}</h3>
                </div>
                <p className='mt-2 max-w-[34ch] text-xs leading-5 text-muted'>{description}</p>
            </div>
            <div className='min-w-0'>{children}</div>
        </section>
    );
}

export function SettingToggle({
    label,
    description,
    selected,
    onChange,
}: {
    label: string;
    description: string;
    selected: boolean;
    onChange: (selected: boolean) => void;
}) {
    return (
        <div className='flex items-center justify-between gap-4 rounded-lg border border-border/70 bg-surface-secondary/20 px-4 py-3'>
            <div>
                <div className='text-sm font-medium'>{label}</div>
                <div className='mt-0.5 text-xs leading-5 text-muted'>{description}</div>
            </div>
            <ToggleSwitch label={label} isSelected={selected} onChange={onChange} />
        </div>
    );
}

function SettingsCard({
    icon: Icon,
    title,
    description,
    children,
}: {
    icon: typeof HardDrive;
    title: string;
    description: string;
    children: React.ReactNode;
}) {
    return (
        <ContentCard className='overflow-hidden' noPadding>
            <div className='flex items-center gap-2.5 border-b border-border px-5 py-4 lg:px-6'>
                <span className='flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-surface-secondary text-muted'>
                    <Icon className='h-4 w-4' />
                </span>
                <div>
                    <h2 className='text-sm font-semibold'>{title}</h2>
                    <p className='mt-0.5 text-xs leading-5 text-muted'>{description}</p>
                </div>
            </div>
            <div className='px-5 py-6 lg:px-6'>{children}</div>
        </ContentCard>
    );
}

const cookieNamePattern = /^[A-Za-z0-9_!#$%&'*+.^`|~-]+$/;

function EditCacheKeyDialog({
    parts,
    headers,
    cookies,
    queryNormalize,
    onClose,
    onConfirm,
}: {
    parts: CacheKeyPart[];
    headers: string[];
    cookies: string[];
    queryNormalize: boolean;
    onClose: () => void;
    onConfirm: (next: {
        parts: CacheKeyPart[];
        headers: string[];
        cookies: string[];
        queryNormalize: boolean;
    }) => void;
}) {
    const [selectedParts, setSelectedParts] = useState<CacheKeyPart[]>(parts);
    const [selectedHeaders, setSelectedHeaders] = useState<string[]>(headers);
    const [selectedCookies, setSelectedCookies] = useState<string[]>(cookies);
    const [normalizeQuery, setNormalizeQuery] = useState(queryNormalize);
    const [headerDraft, setHeaderDraft] = useState('');
    const [cookieDraft, setCookieDraft] = useState('');
    const normalizedHeader = canonicalHeaderName(headerDraft);
    const headerManaged = normalizedHeader.toLowerCase() === 'accept-encoding';
    const headerValid =
        /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(headerDraft.trim()) &&
        !headerManaged &&
        !selectedHeaders.some((header) => header.toLowerCase() === normalizedHeader.toLowerCase());
    const cookieName = cookieDraft.trim();
    const cookieValid =
        cookieNamePattern.test(cookieName) &&
        !selectedCookies.some((cookie) => cookie === cookieName);
    const addHeader = () => {
        if (!headerValid) return;
        setSelectedHeaders([...selectedHeaders, normalizedHeader]);
        setHeaderDraft('');
    };
    const addCookie = () => {
        if (!cookieValid) return;
        setSelectedCookies([...selectedCookies, cookieName]);
        setCookieDraft('');
    };
    const confirm = () => {
        const orderedParts = cacheKeyPartOrder.filter((part) => selectedParts.includes(part));
        onConfirm({
            parts: orderedParts,
            headers: selectedHeaders,
            cookies: [...selectedCookies].sort((a, b) => a.localeCompare(b)),
            queryNormalize: normalizeQuery,
        });
    };

    return (
        <DialogShell
            icon={<Braces className='h-4 w-4' />}
            isOpen
            size='md'
            title='Edit key template'
            subtitle='Select every request value that should distinguish cached objects.'
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
        >
            <div className='max-h-[65vh] space-y-6 overflow-y-auto px-6 py-5'>
                <div>
                    <div className='mb-2 text-xs font-semibold text-muted'>Request values</div>
                    <CheckboxGroup
                        className='grid gap-2'
                        value={selectedParts}
                        onChange={(values) =>
                            setSelectedParts(
                                cacheKeyPartOrder.filter(
                                    (part) => part === 'PATH' || values.includes(part)
                                )
                            )
                        }
                    >
                        {cacheKeyPartOrder.map((part) => {
                            const detail = cacheKeyPartDetails[part];
                            return (
                                <Checkbox
                                    className='rounded-lg border border-border/70 px-3 py-3 mt-0'
                                    isDisabled={part === 'PATH'}
                                    key={part}
                                    value={part}
                                >
                                    <Checkbox.Content className='flex w-full items-center gap-3'>
                                        <Checkbox.Control>
                                            <Checkbox.Indicator />
                                        </Checkbox.Control>
                                        <span className='min-w-0 flex-1'>
                                            <span className='block text-sm font-medium'>
                                                {detail.label}
                                            </span>
                                            <span className='mt-0.5 block text-xs text-muted'>
                                                {detail.description}
                                            </span>
                                        </span>
                                        <code className='shrink-0 text-xs text-muted'>
                                            {detail.token}
                                        </code>
                                    </Checkbox.Content>
                                </Checkbox>
                            );
                        })}
                    </CheckboxGroup>
                </div>

                <FormField
                    error={
                        headerDraft && !headerValid
                            ? headerManaged
                                ? 'Accept-Encoding is managed by compression and HTTP Vary handling.'
                                : 'Enter a unique HTTP request header name.'
                            : undefined
                    }
                    hint='The header value is appended after the standard request values.'
                    htmlFor='cache-key-header-name'
                    label='Request header'
                >
                    <div className='flex gap-2'>
                        <Input
                            id='cache-key-header-name'
                            placeholder='Accept-Language'
                            value={headerDraft}
                            variant='secondary'
                            onChange={(event) => setHeaderDraft(event.target.value)}
                            onKeyDown={(event) => {
                                if (event.key === 'Enter') {
                                    event.preventDefault();
                                    addHeader();
                                }
                            }}
                        />
                        <Button isDisabled={!headerValid} variant='secondary' onPress={addHeader}>
                            Add header
                        </Button>
                    </div>
                </FormField>
                {selectedHeaders.length > 0 && (
                    <div className='grid gap-2 sm:grid-cols-2'>
                        {selectedHeaders.map((header) => (
                            <div
                                className='flex min-h-11 items-center justify-between gap-2 rounded-lg border border-border/70 px-3 py-2'
                                key={header.toLowerCase()}
                            >
                                <code className='truncate text-xs font-semibold'>{header}</code>
                                <Button
                                    isIconOnly
                                    aria-label={`Remove ${header} from cache key`}
                                    size='sm'
                                    variant='ghost'
                                    onPress={() =>
                                        setSelectedHeaders(
                                            selectedHeaders.filter(
                                                (value) =>
                                                    value.toLowerCase() !== header.toLowerCase()
                                            )
                                        )
                                    }
                                >
                                    <X className='h-3.5 w-3.5' />
                                </Button>
                            </div>
                        ))}
                    </div>
                )}

                {selectedParts.includes('QUERY') && (
                    <SettingToggle
                        description='Sort query parameters by name so ?b=2&a=1 and ?a=1&b=2 share one cache object. On by default for every site unless you turn it off.'
                        label='Normalize query string'
                        selected={normalizeQuery}
                        onChange={setNormalizeQuery}
                    />
                )}

                <FormField
                    error={
                        cookieDraft && !cookieValid
                            ? 'Enter a unique cookie name (no spaces).'
                            : undefined
                    }
                    hint='Cookie values are appended to the key. Prefer low-cardinality flags, not session IDs.'
                    htmlFor='cache-key-cookie-name'
                    label='Request cookie'
                >
                    <div className='flex gap-2'>
                        <Input
                            id='cache-key-cookie-name'
                            placeholder='ab_test'
                            value={cookieDraft}
                            variant='secondary'
                            onChange={(event) => setCookieDraft(event.target.value)}
                            onKeyDown={(event) => {
                                if (event.key === 'Enter') {
                                    event.preventDefault();
                                    addCookie();
                                }
                            }}
                        />
                        <Button isDisabled={!cookieValid} variant='secondary' onPress={addCookie}>
                            Add cookie
                        </Button>
                    </div>
                </FormField>
                {selectedCookies.length > 0 && (
                    <>
                        <div className='rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs leading-5 text-warning'>
                            Each distinct cookie value creates a separate cache object. Session or
                            user-id cookies can exhaust storage and collapse hit rates — use only
                            low-cardinality cookies (feature flags, locale, currency).
                        </div>
                        <div className='grid gap-2 sm:grid-cols-2'>
                            {selectedCookies.map((cookie) => (
                                <div
                                    className='flex min-h-11 items-center justify-between gap-2 rounded-lg border border-border/70 px-3 py-2'
                                    key={cookie}
                                >
                                    <code className='truncate text-xs font-semibold'>{cookie}</code>
                                    <Button
                                        isIconOnly
                                        aria-label={`Remove ${cookie} from cache key`}
                                        size='sm'
                                        variant='ghost'
                                        onPress={() =>
                                            setSelectedCookies(
                                                selectedCookies.filter((value) => value !== cookie)
                                            )
                                        }
                                    >
                                        <X className='h-3.5 w-3.5' />
                                    </Button>
                                </div>
                            ))}
                        </div>
                    </>
                )}
            </div>
            <DialogFooter>
                <Button variant='ghost' onPress={onClose}>
                    Cancel
                </Button>
                <Button onPress={confirm}>Confirm selection</Button>
            </DialogFooter>
        </DialogShell>
    );
}

function AddBypassDirectiveDialog({
    isOpen,
    values,
    onClose,
    onAdd,
}: {
    isOpen: boolean;
    values: string[];
    onClose: () => void;
    onAdd: (value: string) => void;
}) {
    const [customDraft, setCustomDraft] = useState('');
    const normalizedCustom = customDraft.trim().toLowerCase();
    const customValid =
        /^[a-z][a-z0-9_-]*(?:=[a-z0-9._-]+)?$/.test(normalizedCustom) &&
        !values.some((value) => value.toLowerCase() === normalizedCustom);
    const addCustom = () => {
        if (!customValid) return;
        onAdd(normalizedCustom);
        setCustomDraft('');
    };

    return (
        <DialogShell
            icon={<ShieldCheck className='h-4 w-4' />}
            isOpen={isOpen}
            size='md'
            title='Add bypass directive'
            subtitle='Cache lookup and storage are skipped when Cache-Control contains this value.'
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
        >
            <div className='max-h-[65vh] space-y-6 overflow-y-auto px-6 py-5'>
                <div>
                    <div className='mb-2 text-xs font-semibold text-muted'>Common directives</div>
                    <div className='divide-y divide-border border-y border-border'>
                        {bypassPresets.map((preset) => {
                            const included = values.some(
                                (value) => value.toLowerCase() === preset.value
                            );
                            return (
                                <button
                                    className='flex w-full items-center justify-between gap-4 px-1 py-3 text-left transition-colors hover:bg-surface-secondary/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-not-allowed disabled:opacity-45'
                                    disabled={included}
                                    key={preset.value}
                                    type='button'
                                    onClick={() => onAdd(preset.value)}
                                >
                                    <span>
                                        <code className='block text-sm font-semibold'>
                                            {preset.value}
                                        </code>
                                        <span className='mt-0.5 block text-xs text-muted'>
                                            {included ? 'Already configured' : preset.description}
                                        </span>
                                    </span>
                                    <Plus className='h-4 w-4 shrink-0 text-muted' />
                                </button>
                            );
                        })}
                    </div>
                </div>

                <FormField
                    error={
                        customDraft && !customValid
                            ? 'Use a unique directive such as s-maxage=0.'
                            : undefined
                    }
                    htmlFor='cache-bypass-custom'
                    label='Custom directive'
                >
                    <div className='flex gap-2'>
                        <Input
                            id='cache-bypass-custom'
                            placeholder='s-maxage=0'
                            value={customDraft}
                            variant='secondary'
                            onChange={(event) => setCustomDraft(event.target.value)}
                            onKeyDown={(event) => {
                                if (event.key === 'Enter') {
                                    event.preventDefault();
                                    addCustom();
                                }
                            }}
                        />
                        <Button isDisabled={!customValid} variant='secondary' onPress={addCustom}>
                            Add
                        </Button>
                    </div>
                </FormField>
            </div>
            <DialogFooter>
                <Button variant='secondary' onPress={onClose}>
                    Done
                </Button>
            </DialogFooter>
        </DialogShell>
    );
}

export function SiteCacheSettings({
    cache,
    isDirty,
    saving,
    onChange,
    onDiscard,
    onSave,
}: SiteCacheSettingsProps) {
    const [addingKeyPart, setAddingKeyPart] = useState(false);
    const [addingBypass, setAddingBypass] = useState(false);
    const methods = cache.methods ?? ['GET', 'HEAD'];
    const cacheKeyParts = cache.cache_key?.parts ?? [];
    const cacheKeyHeaders = cache.cache_key?.headers ?? [];
    const cacheKeyCookies = cache.cache_key?.cookies ?? [];
    // Omitted normalize defaults to enabled (matches control-plane policy).
    const queryNormalize = cache.cache_key?.query?.normalize !== false;
    const bypassValues = cache.bypass_cache_control ?? [];
    const maxBodyBytes = cache.max_body_bytes ?? 64 << 20;
    const staleValid =
        !cache.stale?.enabled ||
        ((cache.stale.if_error_seconds ?? 0) >= 1 &&
            (cache.stale.if_error_seconds ?? 0) <= maxSeconds &&
            (cache.stale.while_revalidate_seconds ?? 0) >= 0 &&
            (cache.stale.while_revalidate_seconds ?? 0) <= maxSeconds);
    const cacheKeyHeadersValid =
        new Set(cacheKeyHeaders.map((header) => header.toLowerCase())).size ===
            cacheKeyHeaders.length &&
        cacheKeyHeaders.every((header) => /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(header));
    const cacheKeyCookiesValid =
        new Set(cacheKeyCookies).size === cacheKeyCookies.length &&
        cacheKeyCookies.every((cookie) => cookieNamePattern.test(cookie));
    const cacheKeyPartsValid =
        cacheKeyParts.includes('PATH') &&
        new Set(cacheKeyParts).size === cacheKeyParts.length &&
        cacheKeyParts.every((part) => cacheKeyPartOrder.includes(part));
    const bypassValid =
        new Set(bypassValues.map((value) => value.toLowerCase())).size === bypassValues.length &&
        bypassValues.every((value) => /^[a-z][a-z0-9_-]*(?:=[a-z0-9._-]+)?$/i.test(value));
    const policyValid =
        methods.length > 0 &&
        maxBodyBytes >= 1 &&
        maxBodyBytes <= maxBodyLimit &&
        staleValid &&
        cacheKeyPartsValid &&
        cacheKeyHeadersValid &&
        cacheKeyCookiesValid &&
        bypassValid;

    const setCacheKey = (patch: Partial<NonNullable<CachePolicy['cache_key']>>) =>
        onChange({
            ...cache,
            cache_key: {
                ...cache.cache_key,
                parts: cacheKeyParts,
                headers: cacheKeyHeaders,
                cookies: cacheKeyCookies,
                query: {
                    ...cache.cache_key?.query,
                    normalize: queryNormalize,
                },
                ...patch,
            },
        });
    const confirmCacheKey = (next: {
        parts: CacheKeyPart[];
        headers: string[];
        cookies: string[];
        queryNormalize: boolean;
    }) => {
        setCacheKey({
            parts: next.parts,
            headers: next.headers,
            cookies: next.cookies,
            query: {
                ...cache.cache_key?.query,
                normalize: next.queryNormalize,
            },
        });
        setAddingKeyPart(false);
    };
    const addBypassValue = (value: string) => {
        if (bypassValues.some((current) => current.toLowerCase() === value.toLowerCase())) return;
        onChange({ ...cache, bypass_cache_control: [...bypassValues, value] });
        setAddingBypass(false);
    };
    const keyStorageMode = cache.cache_key?.hide
        ? 'hidden'
        : cache.cache_key?.hash
          ? 'hashed'
          : 'readable';
    const cacheKeyTokens = [
        ...cacheKeyParts.map((part) => ({
            id: `part:${part}`,
            label: cacheKeyPartDetails[part].label,
            token: cacheKeyPartDetails[part].token,
            required: part === 'PATH',
        })),
        ...cacheKeyHeaders.map((header) => ({
            id: `header:${header.toLowerCase()}`,
            label: `Header: ${header}`,
            token: `{header.${header.toLowerCase()}}`,
            required: false,
        })),
        ...cacheKeyCookies.map((cookie) => ({
            id: `cookie:${cookie}`,
            label: `Cookie: ${cookie}`,
            token: `{cookie.${cookie}}`,
            required: false,
        })),
    ];
    const cacheKeyPreview = cacheKeyTokens.map((item) => item.token).join(' + ');

    return (
        <>
            <div className='space-y-4'>
                <SettingsCard
                    icon={KeyRound}
                    title='Cache key'
                    description='Build the request identity used to find cached objects. Parts are applied in the order shown.'
                >
                    <div className='space-y-5'>
                        <fieldset>
                            <legend className='text-sm font-medium'>Stored key format</legend>
                            <p className='mt-0.5 text-xs text-muted'>
                                Hashing shortens stored keys. Hidden keys are omitted from
                                Cache-Status.
                            </p>
                            <div className='mt-2 inline-flex max-w-full overflow-x-auto rounded-lg border border-border p-1'>
                                {[
                                    {
                                        id: 'readable',
                                        label: 'Readable',
                                        hash: false,
                                        hide: false,
                                    },
                                    { id: 'hashed', label: 'Hashed', hash: true, hide: false },
                                    {
                                        id: 'hidden',
                                        label: 'Hashed + hidden',
                                        hash: true,
                                        hide: true,
                                    },
                                ].map((option) => (
                                    <Button
                                        aria-pressed={keyStorageMode === option.id}
                                        className='shrink-0'
                                        key={option.id}
                                        size='sm'
                                        variant={keyStorageMode === option.id ? 'primary' : 'ghost'}
                                        onPress={() =>
                                            setCacheKey({
                                                hash: option.hash,
                                                hide: option.hide,
                                            })
                                        }
                                    >
                                        {option.id !== 'readable' && (
                                            <Hash className='mr-1.5 h-3.5 w-3.5' />
                                        )}
                                        {option.label}
                                    </Button>
                                ))}
                            </div>
                        </fieldset>

                        <div>
                            <div className='mb-2 flex items-center justify-between gap-3'>
                                <div>
                                    <div className='text-sm font-medium'>Key template</div>
                                    <div className='mt-0.5 text-xs text-muted'>
                                        Every selected value must match before an object is reused.
                                    </div>
                                </div>
                                <Button
                                    size='sm'
                                    variant='secondary'
                                    onPress={() => setAddingKeyPart(true)}
                                >
                                    <Edit3 className='mr-1.5 h-4 w-4' /> Edit template
                                </Button>
                            </div>
                            <div className='flex min-h-14 flex-wrap items-center gap-2 py-1'>
                                {cacheKeyTokens.map((item, index) => (
                                    <div className='flex items-center gap-2' key={item.id}>
                                        {index > 0 && (
                                            <span aria-hidden='true' className='text-sm text-muted'>
                                                +
                                            </span>
                                        )}
                                        <div className='group flex min-h-14 items-center gap-2 rounded-lg border border-border bg-surface px-3 py-2'>
                                            <div className='min-w-0'>
                                                <div className='text-[11px] font-medium text-muted'>
                                                    {item.label}
                                                </div>
                                                <code className='block max-w-52 truncate text-xs font-semibold text-foreground'>
                                                    {item.token}
                                                </code>
                                            </div>
                                            {item.required ? (
                                                <span title='Required by the cache engine'>
                                                    <LockKeyhole className='h-3.5 w-3.5 text-muted' />
                                                </span>
                                            ) : null}
                                        </div>
                                    </div>
                                ))}
                            </div>
                            <div className='mt-3 rounded-lg bg-surface-secondary/45 px-4 py-3'>
                                <div className='mb-1.5 text-xs font-medium text-muted'>
                                    Key structure preview
                                </div>
                                <code className='block overflow-x-auto whitespace-nowrap text-xs font-semibold text-foreground'>
                                    {cacheKeyPreview || 'No key parts selected'}
                                </code>
                                {cacheKeyParts.includes('QUERY') && (
                                    <p className='mt-2 text-xs text-muted'>
                                        Query string:{' '}
                                        {queryNormalize
                                            ? 'parameters are sorted by name (default).'
                                            : 'raw order is preserved.'}
                                    </p>
                                )}
                            </div>
                            {cacheKeyCookies.length > 0 && (
                                <div className='mt-3 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs leading-5 text-warning'>
                                    Cookie values are part of the cache key. High-cardinality
                                    cookies (session, user id) create one object per visitor and can
                                    exhaust cache storage.
                                </div>
                            )}
                            {(!cacheKeyPartsValid ||
                                !cacheKeyHeadersValid ||
                                !cacheKeyCookiesValid) && (
                                <p className='mt-2 text-xs text-danger'>
                                    The key must contain URI path and use unique, valid header and
                                    cookie names.
                                </p>
                            )}
                        </div>
                    </div>
                </SettingsCard>

                <SettingsCard
                    icon={ShieldCheck}
                    title='Cache bypass'
                    description='Skip both cache lookup and storage when a request or response contains a matching Cache-Control directive.'
                >
                    <div className='space-y-3'>
                        <div className='flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between'>
                            <div className='text-sm font-medium'>Cache-Control directives</div>
                            <Button
                                size='sm'
                                variant='secondary'
                                onPress={() => setAddingBypass(true)}
                            >
                                <Plus className='mr-1.5 h-4 w-4' /> Add directive
                            </Button>
                        </div>
                        {bypassValues.length > 0 ? (
                            <div className='grid gap-3 sm:grid-cols-2'>
                                {bypassValues.map((value) => {
                                    const description = bypassPresets.find(
                                        (preset) => preset.value === value.toLowerCase()
                                    )?.description;
                                    return (
                                        <div
                                            className='flex items-start justify-between gap-3 rounded-lg border border-border/70 bg-surface-secondary/20 px-4 py-3'
                                            key={value.toLowerCase()}
                                        >
                                            <div className='min-w-0'>
                                                <code className='block truncate text-sm font-semibold'>
                                                    {value}
                                                </code>
                                                <p className='mt-1 text-xs leading-5 text-muted'>
                                                    {description ??
                                                        'Bypass cache when this custom directive is present.'}
                                                </p>
                                            </div>
                                            <Button
                                                isIconOnly
                                                aria-label={`Remove ${value} bypass directive`}
                                                className='shrink-0'
                                                size='sm'
                                                variant='ghost'
                                                onPress={() =>
                                                    onChange({
                                                        ...cache,
                                                        bypass_cache_control: bypassValues.filter(
                                                            (current) => current !== value
                                                        ),
                                                    })
                                                }
                                            >
                                                <Trash2 className='h-4 w-4 text-danger' />
                                            </Button>
                                        </div>
                                    );
                                })}
                            </div>
                        ) : (
                            <div className='rounded-lg border border-dashed border-border px-4 py-4 text-sm text-muted'>
                                No Cache-Control directive currently bypasses the cache.
                            </div>
                        )}
                        {!bypassValid && (
                            <p className='text-xs text-danger'>
                                Directives must be unique values such as no-store or max-age=0.
                            </p>
                        )}
                    </div>
                </SettingsCard>

                <SettingsCard
                    icon={ShieldCheck}
                    title='Cache behavior'
                    description='Set cacheable methods, object limits, stale delivery, and response diagnostics.'
                >
                    <div className='space-y-4'>
                        <fieldset aria-label='Cacheable methods' className='flex flex-wrap gap-2'>
                            {cacheMethods.map((method) => {
                                const selected = methods.includes(method);
                                return (
                                    <Button
                                        aria-pressed={selected}
                                        className='min-w-0 px-2.5 font-mono text-xs'
                                        key={method}
                                        size='sm'
                                        variant={selected ? 'primary' : 'secondary'}
                                        onPress={() => {
                                            const next = selected
                                                ? methods.filter((current) => current !== method)
                                                : [...methods, method];
                                            onChange({
                                                ...cache,
                                                methods: next,
                                                request_coalescing:
                                                    cache.request_coalescing !== false &&
                                                    next.every((current) =>
                                                        ['GET', 'HEAD', 'OPTIONS'].includes(current)
                                                    ),
                                            });
                                        }}
                                    >
                                        {method}
                                    </Button>
                                );
                            })}
                        </fieldset>
                        <div className='grid gap-3 md:grid-cols-2'>
                            <SettingToggle
                                label='Request coalescing'
                                description='Combine simultaneous misses for safe methods.'
                                selected={cache.request_coalescing ?? true}
                                onChange={(request_coalescing) =>
                                    onChange({ ...cache, request_coalescing })
                                }
                            />
                            <SettingToggle
                                label='Cache range requests'
                                description='Store valid single byte-range responses.'
                                selected={cache.cache_range_requests ?? true}
                                onChange={(cache_range_requests) =>
                                    onChange({ ...cache, cache_range_requests })
                                }
                            />
                            <SettingToggle
                                label='Expose X-Cache'
                                description='Return HIT, MISS, STALE, or BYPASS.'
                                selected={cache.response_headers?.x_cache ?? true}
                                onChange={(x_cache) =>
                                    onChange({
                                        ...cache,
                                        response_headers: {
                                            ...cache.response_headers,
                                            x_cache,
                                            age: cache.response_headers?.age ?? true,
                                        },
                                    })
                                }
                            />
                            <SettingToggle
                                label='Expose Age'
                                description='Return current cached response age.'
                                selected={cache.response_headers?.age ?? true}
                                onChange={(age) =>
                                    onChange({
                                        ...cache,
                                        response_headers: {
                                            ...cache.response_headers,
                                            x_cache: cache.response_headers?.x_cache ?? true,
                                            age,
                                        },
                                    })
                                }
                            />
                            <SettingToggle
                                label='Allow PURGE method'
                                description='Enable authenticated cache invalidation.'
                                selected={cache.allow_purge_method ?? false}
                                onChange={(allow_purge_method) =>
                                    onChange({ ...cache, allow_purge_method })
                                }
                            />
                            <SettingToggle
                                label='Serve stale on error'
                                description='Use expired content during origin failures.'
                                selected={cache.stale?.enabled ?? true}
                                onChange={(enabled) =>
                                    onChange({
                                        ...cache,
                                        stale: {
                                            ...cache.stale,
                                            enabled,
                                            if_error_seconds:
                                                cache.stale?.if_error_seconds ?? 86400,
                                            while_revalidate_seconds:
                                                cache.stale?.while_revalidate_seconds ?? 30,
                                        },
                                    })
                                }
                            />
                        </div>
                        <div className='grid gap-4 md:grid-cols-3'>
                            <FormField
                                error={
                                    maxBodyBytes >= 1 && maxBodyBytes <= maxBodyLimit
                                        ? undefined
                                        : 'Choose a size from 1 B to 4 GB.'
                                }
                                htmlFor='cache-max-body'
                                label='Maximum body size'
                            >
                                <ByteSizeInput
                                    id='cache-max-body'
                                    bytes={maxBodyBytes}
                                    defaultUnit='MB'
                                    minimumBytes={1}
                                    maximumBytes={maxBodyLimit}
                                    onChange={(max_body_bytes) =>
                                        onChange({
                                            ...cache,
                                            max_body_bytes,
                                        })
                                    }
                                />
                            </FormField>
                            {cache.stale?.enabled && (
                                <>
                                    <FormField
                                        error={
                                            (cache.stale.if_error_seconds ?? 0) >= 1 &&
                                            (cache.stale.if_error_seconds ?? 0) <= maxSeconds
                                                ? undefined
                                                : 'Choose a duration from 1 second to 365 days.'
                                        }
                                        htmlFor='cache-stale-error'
                                        label='Stale on error'
                                    >
                                        <DurationInput
                                            id='cache-stale-error'
                                            seconds={cache.stale.if_error_seconds ?? 86400}
                                            minimumSeconds={1}
                                            maximumSeconds={maxSeconds}
                                            onChange={(if_error_seconds) =>
                                                onChange({
                                                    ...cache,
                                                    stale: {
                                                        ...cache.stale,
                                                        enabled: true,
                                                        if_error_seconds,
                                                    },
                                                })
                                            }
                                        />
                                    </FormField>
                                    <FormField
                                        error={
                                            (cache.stale.while_revalidate_seconds ?? 0) >= 0 &&
                                            (cache.stale.while_revalidate_seconds ?? 0) <=
                                                maxSeconds
                                                ? undefined
                                                : 'Choose a duration from 0 seconds to 365 days.'
                                        }
                                        htmlFor='cache-swr'
                                        label='Stale while revalidate'
                                    >
                                        <DurationInput
                                            id='cache-swr'
                                            seconds={cache.stale.while_revalidate_seconds ?? 30}
                                            minimumSeconds={0}
                                            maximumSeconds={maxSeconds}
                                            onChange={(while_revalidate_seconds) =>
                                                onChange({
                                                    ...cache,
                                                    stale: {
                                                        ...cache.stale,
                                                        enabled: true,
                                                        while_revalidate_seconds,
                                                    },
                                                })
                                            }
                                        />
                                    </FormField>
                                </>
                            )}
                        </div>
                    </div>
                </SettingsCard>

                <SettingsActionBar
                    isDirty={isDirty}
                    isDiscardDisabled={saving}
                    onDiscard={onDiscard}
                >
                    <Button isDisabled={saving || !policyValid} onPress={onSave}>
                        <Save className='mr-1.5 h-4 w-4' />
                        {saving ? 'Saving...' : 'Save cache settings'}
                    </Button>
                </SettingsActionBar>
            </div>

            {addingKeyPart && (
                <EditCacheKeyDialog
                    cookies={cacheKeyCookies}
                    headers={cacheKeyHeaders}
                    parts={cacheKeyParts}
                    queryNormalize={queryNormalize}
                    onClose={() => setAddingKeyPart(false)}
                    onConfirm={confirmCacheKey}
                />
            )}
            <AddBypassDirectiveDialog
                isOpen={addingBypass}
                values={bypassValues}
                onAdd={addBypassValue}
                onClose={() => setAddingBypass(false)}
            />
        </>
    );
}
