import type {
    DeliveryOrigin,
    DeliveryPolicy,
    HeaderRule,
    PathOriginPool,
    TrafficSplitRule,
} from '@/api';

import { Button, Input, TextArea } from '@heroui/react';
import {
    AlertTriangle,
    ArrowRightLeft,
    Braces,
    ChevronRight,
    FileWarning,
    GitBranch,
    Globe2,
    Network,
    Plus,
    Route,
    Save,
    Trash2,
    Wrench,
} from 'lucide-react';
import { useRef } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { FormField } from '@/components/FormField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { SettingsActionBar } from '@/components/SettingsActionBar.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { normalizeDeliveryPolicy } from '@/utils/delivery.ts';

interface Props {
    policy: DeliveryPolicy;
    isDirty: boolean;
    saving: boolean;
    onChange: (policy: DeliveryPolicy) => void;
    onDiscard: () => void;
    onSave: () => void;
}

type RewriteRule = DeliveryPolicy['rewrites'][number];
type RedirectRule = DeliveryPolicy['redirects'][number];
type ErrorPage = DeliveryPolicy['error_pages'][number];

function splitLines(value: string) {
    return value
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean);
}

function splitStatuses(value: string) {
    return splitLines(value).map(Number).filter(Number.isFinite);
}

function newKey() {
    return crypto.randomUUID();
}

function useStableKeys() {
    const keys = useRef<string[]>([]);
    return (index: number) => {
        keys.current[index] ??= newKey();
        return keys.current[index];
    };
}

function Section({
    icon: Icon,
    title,
    description,
    children,
}: {
    icon: typeof Route;
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

function EmptyRows({ children }: { children: React.ReactNode }) {
    return (
        <div className='rounded-lg border border-dashed border-border bg-surface-secondary/20 px-4 py-5 text-center text-sm text-muted'>
            {children}
        </div>
    );
}

function RemoveButton({ label, onPress }: { label: string; onPress: () => void }) {
    return (
        <Button isIconOnly aria-label={label} size='sm' variant='ghost' onPress={onPress}>
            <Trash2 className='h-4 w-4 text-danger' />
        </Button>
    );
}

function EditorHeader({
    title,
    description,
    actionLabel,
    onAdd,
}: {
    title: string;
    description?: string;
    actionLabel: string;
    onAdd: () => void;
}) {
    return (
        <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
            <div>
                <h4 className='text-sm font-semibold'>{title}</h4>
                {description && <p className='mt-1 text-xs leading-5 text-muted'>{description}</p>}
            </div>
            <Button className='shrink-0 self-start' size='sm' variant='secondary' onPress={onAdd}>
                <Plus className='mr-1.5 h-3.5 w-3.5' />
                {actionLabel}
            </Button>
        </div>
    );
}

function HeaderEditor({
    label,
    description,
    rules,
    onChange,
}: {
    label: string;
    description: string;
    rules: HeaderRule[];
    onChange: (rules: HeaderRule[]) => void;
}) {
    const keyAt = useStableKeys();
    const update = (index: number, patch: Partial<HeaderRule>) =>
        onChange(rules.map((rule, current) => (current === index ? { ...rule, ...patch } : rule)));

    return (
        <div className='space-y-3'>
            <EditorHeader
                actionLabel='Add header'
                description={description}
                title={label}
                onAdd={() => onChange([...rules, { operation: 'SET', name: '', value: '' }])}
            />
            {rules.length === 0 && <EmptyRows>No custom headers configured.</EmptyRows>}
            {rules.map((rule, index) => (
                <div
                    className='grid gap-2 rounded-lg border border-border/70 bg-surface-secondary/20 p-3 sm:grid-cols-[104px_1fr_1.4fr_32px]'
                    key={keyAt(index)}
                >
                    <SelectField
                        ariaLabel={`${label} operation ${index + 1}`}
                        options={[
                            { id: 'SET', label: 'Set' },
                            { id: 'ADD', label: 'Add' },
                            { id: 'DELETE', label: 'Delete' },
                        ]}
                        value={rule.operation}
                        variant='secondary'
                        onChange={(value) =>
                            update(index, {
                                operation: value as HeaderRule['operation'],
                            })
                        }
                    />
                    <Input
                        aria-label={`${label} name ${index + 1}`}
                        placeholder='X-Header-Name'
                        value={rule.name}
                        variant='secondary'
                        onChange={(event) => update(index, { name: event.target.value })}
                    />
                    <Input
                        aria-label={`${label} value ${index + 1}`}
                        disabled={rule.operation === 'DELETE'}
                        placeholder={
                            rule.operation === 'DELETE' ? 'No value required' : 'Header value'
                        }
                        value={rule.value || ''}
                        variant='secondary'
                        onChange={(event) => update(index, { value: event.target.value })}
                    />
                    <RemoveButton
                        label={`Remove ${label.toLowerCase()} ${index + 1}`}
                        onPress={() => onChange(rules.filter((_, current) => current !== index))}
                    />
                </div>
            ))}
        </div>
    );
}

function URLRulesEditor({
    rewrites,
    redirects,
    onRewritesChange,
    onRedirectsChange,
}: {
    rewrites: RewriteRule[];
    redirects: RedirectRule[];
    onRewritesChange: (rules: RewriteRule[]) => void;
    onRedirectsChange: (rules: RedirectRule[]) => void;
}) {
    const rewriteKeyAt = useStableKeys();
    const redirectKeyAt = useStableKeys();
    return (
        <div className='space-y-7'>
            <div className='space-y-3'>
                <EditorHeader
                    actionLabel='Add rewrite'
                    description='Change the upstream path without changing the URL shown to the visitor.'
                    title='Rewrites'
                    onAdd={() => onRewritesChange([...rewrites, { path: '', replacement: '' }])}
                />
                {rewrites.length === 0 && (
                    <EmptyRows>Requests keep their original paths.</EmptyRows>
                )}
                {rewrites.map((rule, index) => (
                    <div
                        className='grid gap-2 rounded-lg border border-border/70 bg-surface-secondary/20 p-3 sm:grid-cols-[1fr_24px_1fr_32px] sm:items-center'
                        key={rewriteKeyAt(index)}
                    >
                        <Input
                            aria-label={`Rewrite source path ${index + 1}`}
                            placeholder='/legacy/*'
                            value={rule.path}
                            variant='secondary'
                            onChange={(event) =>
                                onRewritesChange(
                                    rewrites.map((item, current) =>
                                        current === index
                                            ? { ...item, path: event.target.value }
                                            : item
                                    )
                                )
                            }
                        />
                        <ChevronRight className='hidden h-4 w-4 justify-self-center text-muted sm:block' />
                        <Input
                            aria-label={`Rewrite destination path ${index + 1}`}
                            placeholder='/current{http.request.uri.path}'
                            value={rule.replacement}
                            variant='secondary'
                            onChange={(event) =>
                                onRewritesChange(
                                    rewrites.map((item, current) =>
                                        current === index
                                            ? { ...item, replacement: event.target.value }
                                            : item
                                    )
                                )
                            }
                        />
                        <RemoveButton
                            label={`Remove rewrite ${index + 1}`}
                            onPress={() =>
                                onRewritesChange(rewrites.filter((_, current) => current !== index))
                            }
                        />
                    </div>
                ))}
            </div>

            <div className='space-y-3 border-t border-border pt-6'>
                <EditorHeader
                    actionLabel='Add redirect'
                    description='Send visitors to another path or URL with an HTTP redirect response.'
                    title='Redirects'
                    onAdd={() =>
                        onRedirectsChange([...redirects, { path: '', location: '', status: 308 }])
                    }
                />
                {redirects.length === 0 && (
                    <EmptyRows>No visitor-facing redirects configured.</EmptyRows>
                )}
                {redirects.map((rule, index) => (
                    <div
                        className='grid gap-2 rounded-lg border border-border/70 bg-surface-secondary/20 p-3 sm:grid-cols-[1fr_1fr_92px_32px]'
                        key={redirectKeyAt(index)}
                    >
                        <Input
                            aria-label={`Redirect source path ${index + 1}`}
                            placeholder='/old/*'
                            value={rule.path}
                            variant='secondary'
                            onChange={(event) =>
                                onRedirectsChange(
                                    redirects.map((item, current) =>
                                        current === index
                                            ? { ...item, path: event.target.value }
                                            : item
                                    )
                                )
                            }
                        />
                        <Input
                            aria-label={`Redirect destination ${index + 1}`}
                            placeholder='/new'
                            value={rule.location}
                            variant='secondary'
                            onChange={(event) =>
                                onRedirectsChange(
                                    redirects.map((item, current) =>
                                        current === index
                                            ? { ...item, location: event.target.value }
                                            : item
                                    )
                                )
                            }
                        />
                        <SelectField
                            ariaLabel={`Redirect status ${index + 1}`}
                            options={[301, 302, 307, 308].map((status) => ({
                                id: String(status),
                                label: String(status),
                            }))}
                            value={String(rule.status)}
                            variant='secondary'
                            onChange={(value) =>
                                onRedirectsChange(
                                    redirects.map((item, current) =>
                                        current === index
                                            ? { ...item, status: Number(value) }
                                            : item
                                    )
                                )
                            }
                        />
                        <RemoveButton
                            label={`Remove redirect ${index + 1}`}
                            onPress={() =>
                                onRedirectsChange(
                                    redirects.filter((_, current) => current !== index)
                                )
                            }
                        />
                    </div>
                ))}
            </div>
        </div>
    );
}

function OriginEditor({
    origin,
    index,
    onChange,
    onRemove,
}: {
    origin: DeliveryOrigin;
    index: number;
    onChange: (origin: DeliveryOrigin) => void;
    onRemove: () => void;
}) {
    return (
        <div className='grid gap-2 rounded-lg bg-surface p-3 sm:grid-cols-[92px_1.2fr_1fr_82px_32px]'>
            <SelectField
                ariaLabel={`Origin protocol ${index + 1}`}
                options={[
                    { id: 'http', label: 'HTTP' },
                    { id: 'https', label: 'HTTPS' },
                ]}
                value={origin.protocol}
                variant='secondary'
                onChange={(value) =>
                    onChange({
                        ...origin,
                        protocol: value as DeliveryOrigin['protocol'],
                    })
                }
            />
            <Input
                aria-label={`Origin address ${index + 1}`}
                placeholder='api.internal:443'
                value={origin.address}
                variant='secondary'
                onChange={(event) => onChange({ ...origin, address: event.target.value })}
            />
            <Input
                aria-label={`Origin host header ${index + 1}`}
                placeholder='Host header (optional)'
                value={origin.host_header || ''}
                variant='secondary'
                onChange={(event) => onChange({ ...origin, host_header: event.target.value })}
            />
            <Input
                aria-label={`Origin weight ${index + 1}`}
                min={1}
                placeholder='Weight'
                type='number'
                value={String(origin.weight ?? 1)}
                variant='secondary'
                onChange={(event) => onChange({ ...origin, weight: Number(event.target.value) })}
            />
            <RemoveButton label={`Remove origin ${index + 1}`} onPress={onRemove} />
        </div>
    );
}

function OriginPoolsEditor({
    pools,
    onChange,
}: {
    pools: PathOriginPool[];
    onChange: (pools: PathOriginPool[]) => void;
}) {
    const keyAt = useStableKeys();
    const originKeys = useRef<string[][]>([]);
    const originKeyAt = (poolIndex: number, originIndex: number) => {
        originKeys.current[poolIndex] ??= [];
        originKeys.current[poolIndex][originIndex] ??= newKey();
        return originKeys.current[poolIndex][originIndex];
    };
    const updatePool = (index: number, next: PathOriginPool) =>
        onChange(pools.map((pool, current) => (current === index ? next : pool)));

    return (
        <div className='space-y-3'>
            <EditorHeader
                actionLabel='Add pool'
                description='Route matching paths to a dedicated group of upstream servers.'
                title='Origin pools'
                onAdd={() =>
                    onChange([
                        ...pools,
                        {
                            name: '',
                            paths: [],
                            scheduler: 'round_robin',
                            origins: [{ protocol: 'https', address: '', weight: 1 }],
                        },
                    ])
                }
            />
            {pools.length === 0 && (
                <EmptyRows>All requests use the site's default origins.</EmptyRows>
            )}
            {pools.map((pool, poolIndex) => (
                <div className='rounded-lg border border-border/70 p-4' key={keyAt(poolIndex)}>
                    <div className='grid gap-3 sm:grid-cols-[1fr_1fr_160px_32px]'>
                        <FormField label='Pool name' required>
                            <Input
                                placeholder='api'
                                value={pool.name}
                                variant='secondary'
                                onChange={(event) =>
                                    updatePool(poolIndex, { ...pool, name: event.target.value })
                                }
                            />
                        </FormField>
                        <FormField label='Matching paths' required>
                            <Input
                                placeholder='/api/*, /graphql'
                                value={pool.paths.join(', ')}
                                variant='secondary'
                                onChange={(event) =>
                                    updatePool(poolIndex, {
                                        ...pool,
                                        paths: splitLines(event.target.value),
                                    })
                                }
                            />
                        </FormField>
                        <FormField label='Load balancing'>
                            <SelectField
                                ariaLabel={`Origin pool ${poolIndex + 1} load balancing`}
                                className='w-full'
                                options={[
                                    { id: 'round_robin', label: 'Round robin' },
                                    {
                                        id: 'weighted_round_robin',
                                        label: 'Weighted round robin',
                                    },
                                    { id: 'first', label: 'First available' },
                                    { id: 'random', label: 'Random' },
                                    { id: 'least_conn', label: 'Least connections' },
                                    { id: 'ip_hash', label: 'Client IP hash' },
                                ]}
                                value={pool.scheduler || 'round_robin'}
                                variant='secondary'
                                onChange={(value) =>
                                    updatePool(poolIndex, {
                                        ...pool,
                                        scheduler: value,
                                    })
                                }
                            />
                        </FormField>
                        <div className='pt-6'>
                            <RemoveButton
                                label={`Remove pool ${poolIndex + 1}`}
                                onPress={() =>
                                    onChange(pools.filter((_, current) => current !== poolIndex))
                                }
                            />
                        </div>
                    </div>
                    <div className='mt-4 space-y-2 border-t border-border pt-4'>
                        <div className='flex items-center justify-between gap-3'>
                            <span className='text-xs font-medium text-muted'>Upstream servers</span>
                            <Button
                                size='sm'
                                variant='ghost'
                                onPress={() =>
                                    updatePool(poolIndex, {
                                        ...pool,
                                        origins: [
                                            ...pool.origins,
                                            { protocol: 'https', address: '', weight: 1 },
                                        ],
                                    })
                                }
                            >
                                <Plus className='mr-1.5 h-3.5 w-3.5' /> Add origin
                            </Button>
                        </div>
                        {pool.origins.map((origin, originIndex) => (
                            <OriginEditor
                                index={originIndex}
                                key={originKeyAt(poolIndex, originIndex)}
                                origin={origin}
                                onChange={(next) =>
                                    updatePool(poolIndex, {
                                        ...pool,
                                        origins: pool.origins.map((item, current) =>
                                            current === originIndex ? next : item
                                        ),
                                    })
                                }
                                onRemove={() =>
                                    updatePool(poolIndex, {
                                        ...pool,
                                        origins: pool.origins.filter(
                                            (_, current) => current !== originIndex
                                        ),
                                    })
                                }
                            />
                        ))}
                    </div>
                </div>
            ))}
        </div>
    );
}

function SplitsEditor({
    splits,
    poolNames,
    onChange,
}: {
    splits: TrafficSplitRule[];
    poolNames: string[];
    onChange: (splits: TrafficSplitRule[]) => void;
}) {
    const keyAt = useStableKeys();
    const update = (index: number, patch: Partial<TrafficSplitRule>) =>
        onChange(
            splits.map((split, current) => (current === index ? { ...split, ...patch } : split))
        );

    return (
        <div className='space-y-3'>
            <EditorHeader
                actionLabel='Add split'
                description='Select an origin pool using a request value or a stable traffic percentage.'
                title='Traffic splits'
                onAdd={() =>
                    onChange([...splits, { name: '', pool: poolNames[0] || '', percentage: 0 }])
                }
            />
            {splits.length === 0 && (
                <EmptyRows>No conditional traffic splits configured.</EmptyRows>
            )}
            {splits.map((split, index) => (
                <div
                    className='grid gap-3 rounded-lg border border-border/70 bg-surface-secondary/20 p-3 sm:grid-cols-2 xl:grid-cols-[1fr_1fr_1fr_1fr_1fr_100px_32px]'
                    key={keyAt(index)}
                >
                    <Input
                        aria-label={`Split name ${index + 1}`}
                        placeholder='canary'
                        value={split.name}
                        variant='secondary'
                        onChange={(event) => update(index, { name: event.target.value })}
                    />
                    <SelectField
                        ariaLabel={`Split pool ${index + 1}`}
                        options={[
                            { id: '', label: 'Select pool' },
                            ...poolNames.map((name) => ({ id: name, label: name })),
                        ]}
                        value={split.pool}
                        variant='secondary'
                        onChange={(value) => update(index, { pool: value })}
                    />
                    <Input
                        aria-label={`Split header ${index + 1}`}
                        placeholder='Header name'
                        value={split.header_name || ''}
                        variant='secondary'
                        onChange={(event) => update(index, { header_name: event.target.value })}
                    />
                    <Input
                        aria-label={`Split cookie ${index + 1}`}
                        placeholder='Cookie name'
                        value={split.cookie_name || ''}
                        variant='secondary'
                        onChange={(event) => update(index, { cookie_name: event.target.value })}
                    />
                    <Input
                        aria-label={`Split match value ${index + 1}`}
                        placeholder='Match value'
                        value={split.value || ''}
                        variant='secondary'
                        onChange={(event) => update(index, { value: event.target.value })}
                    />
                    <Input
                        aria-label={`Split percentage ${index + 1}`}
                        max={100}
                        min={0}
                        placeholder='Percent'
                        type='number'
                        value={String(split.percentage ?? 0)}
                        variant='secondary'
                        onChange={(event) =>
                            update(index, { percentage: Number(event.target.value) })
                        }
                    />
                    <RemoveButton
                        label={`Remove traffic split ${index + 1}`}
                        onPress={() => onChange(splits.filter((_, current) => current !== index))}
                    />
                </div>
            ))}
            {splits.length > 0 && poolNames.length === 0 && (
                <p className='flex items-center gap-2 text-xs text-danger' role='alert'>
                    <AlertTriangle className='h-3.5 w-3.5' /> Add an origin pool before configuring
                    a split.
                </p>
            )}
        </div>
    );
}

function ErrorPagesEditor({
    pages,
    onChange,
}: {
    pages: ErrorPage[];
    onChange: (pages: ErrorPage[]) => void;
}) {
    const keyAt = useStableKeys();
    const update = (index: number, patch: Partial<ErrorPage>) =>
        onChange(pages.map((page, current) => (current === index ? { ...page, ...patch } : page)));

    return (
        <div className='space-y-3'>
            <EditorHeader
                actionLabel='Add error response'
                description='Return a custom body for one or more 4xx or 5xx responses.'
                title='Custom error responses'
                onAdd={() =>
                    onChange([
                        ...pages,
                        { statuses: [404], content_type: 'text/html; charset=utf-8', body: '' },
                    ])
                }
            />
            {pages.length === 0 && (
                <EmptyRows>Origin error responses pass through unchanged.</EmptyRows>
            )}
            {pages.map((page, index) => (
                <div className='rounded-lg border border-border/70 p-4' key={keyAt(index)}>
                    <div className='grid gap-3 sm:grid-cols-[180px_1fr_32px]'>
                        <FormField label='HTTP statuses' required>
                            <Input
                                placeholder='404, 502, 503'
                                value={page.statuses.join(', ')}
                                variant='secondary'
                                onChange={(event) =>
                                    update(index, { statuses: splitStatuses(event.target.value) })
                                }
                            />
                        </FormField>
                        <FormField label='Content type'>
                            <Input
                                placeholder='text/html; charset=utf-8'
                                value={page.content_type || ''}
                                variant='secondary'
                                onChange={(event) =>
                                    update(index, { content_type: event.target.value })
                                }
                            />
                        </FormField>
                        <div className='pt-6'>
                            <RemoveButton
                                label={`Remove error response ${index + 1}`}
                                onPress={() =>
                                    onChange(pages.filter((_, current) => current !== index))
                                }
                            />
                        </div>
                    </div>
                    <FormField className='mt-3' label='Response body' required>
                        <TextArea
                            className='font-mono text-xs'
                            placeholder='<h1>Not found</h1>'
                            rows={5}
                            value={page.body}
                            variant='secondary'
                            onChange={(event) => update(index, { body: event.target.value })}
                        />
                    </FormField>
                </div>
            ))}
        </div>
    );
}

function policyIsComplete(policy: DeliveryPolicy) {
    const headersValid = [...policy.request_headers, ...policy.response_headers].every(
        (rule) => rule.name.trim() && (rule.operation === 'DELETE' || Boolean(rule.value?.trim()))
    );
    const rewritesValid = policy.rewrites.every(
        (rule) => rule.path.trim() && rule.replacement.trim()
    );
    const redirectsValid = policy.redirects.every(
        (rule) => rule.path.trim() && rule.location.trim()
    );
    const poolsValid = policy.origin_pools.every(
        (pool) =>
            pool.name.trim() &&
            pool.paths.length > 0 &&
            pool.origins.length > 0 &&
            pool.origins.every((origin) => origin.address.trim())
    );
    const poolNames = new Set(policy.origin_pools.map((pool) => pool.name));
    const splitsValid = policy.splits.every(
        (split) =>
            split.name.trim() &&
            poolNames.has(split.pool) &&
            Boolean(split.header_name || split.cookie_name || split.percentage)
    );
    const pagesValid = policy.error_pages.every(
        (page) => page.statuses.length > 0 && page.body.trim()
    );
    return (
        headersValid && rewritesValid && redirectsValid && poolsValid && splitsValid && pagesValid
    );
}

function summary(policy: DeliveryPolicy) {
    const rules =
        policy.rewrites.length +
        policy.redirects.length +
        policy.request_headers.length +
        policy.response_headers.length;
    const routing = policy.origin_pools.length + policy.splits.length;
    return `${rules} request rules, ${routing} routing rules`;
}

export function SiteDeliverySettings({
    policy: inputPolicy,
    isDirty,
    saving,
    onChange,
    onDiscard,
    onSave,
}: Props) {
    const policy = normalizeDeliveryPolicy(inputPolicy);
    const complete = policyIsComplete(policy);
    const poolNames = policy.origin_pools.map((pool) => pool.name.trim()).filter(Boolean);

    return (
        <div className='space-y-8'>
            <ContentCard className='overflow-hidden' noPadding>
                <div className='flex flex-col gap-5 px-5 py-5 sm:flex-row sm:items-center sm:justify-between lg:px-6'>
                    <div className='flex items-center gap-3'>
                        <span className='flex h-10 w-10 items-center justify-center rounded-xl bg-primary text-primary-foreground'>
                            <Route className='h-5 w-5' />
                        </span>
                        <div>
                            <h2 className='text-base font-semibold'>Request delivery</h2>
                            <p className='mt-0.5 text-xs text-muted'>{summary(policy)}</p>
                        </div>
                    </div>
                    <div className='flex items-center gap-3'>
                        {!complete && (
                            <span className='flex items-center gap-1.5 text-xs font-medium text-danger'>
                                <AlertTriangle className='h-3.5 w-3.5' /> Complete required fields
                            </span>
                        )}
                    </div>
                </div>

                <Section
                    description='Temporarily replace site responses or adjust the path sent to your default origins.'
                    icon={Wrench}
                    title='Availability'
                >
                    <div className='space-y-5'>
                        <div className='flex items-center justify-between gap-5 rounded-xl border border-border/70 bg-surface-secondary/20 px-4 py-3.5'>
                            <div>
                                <div className='text-sm font-medium'>Maintenance mode</div>
                                <div className='mt-0.5 text-xs leading-5 text-muted'>
                                    Return a controlled response before any origin request is made.
                                </div>
                            </div>
                            <ToggleSwitch
                                isSelected={policy.maintenance.enabled}
                                label='Maintenance mode'
                                onChange={(enabled) =>
                                    onChange({
                                        ...policy,
                                        maintenance: { ...policy.maintenance, enabled },
                                    })
                                }
                            />
                        </div>
                        {policy.maintenance.enabled && (
                            <div className='grid gap-4 rounded-lg border border-border/70 p-4 sm:grid-cols-[140px_1fr]'>
                                <FormField label='HTTP status'>
                                    <Input
                                        max={599}
                                        min={400}
                                        type='number'
                                        value={String(policy.maintenance.status)}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...policy,
                                                maintenance: {
                                                    ...policy.maintenance,
                                                    status: Number(event.target.value),
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                                <FormField label='Content type'>
                                    <Input
                                        value={policy.maintenance.content_type}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...policy,
                                                maintenance: {
                                                    ...policy.maintenance,
                                                    content_type: event.target.value,
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                                <FormField className='sm:col-span-2' label='Response body'>
                                    <TextArea
                                        rows={5}
                                        value={policy.maintenance.body}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...policy,
                                                maintenance: {
                                                    ...policy.maintenance,
                                                    body: event.target.value,
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                            </div>
                        )}
                        <FormField
                            hint='Leave empty to preserve the incoming request path.'
                            label='Origin path prefix'
                        >
                            <Input
                                placeholder='/production'
                                value={policy.origin_prefix}
                                variant='secondary'
                                onChange={(event) =>
                                    onChange({ ...policy, origin_prefix: event.target.value })
                                }
                            />
                        </FormField>
                    </div>
                </Section>

                <Section
                    description='Rewrite an upstream path or redirect the visitor to a new location.'
                    icon={ArrowRightLeft}
                    title='URL behavior'
                >
                    <URLRulesEditor
                        redirects={policy.redirects}
                        rewrites={policy.rewrites}
                        onRedirectsChange={(redirects) => onChange({ ...policy, redirects })}
                        onRewritesChange={(rewrites) => onChange({ ...policy, rewrites })}
                    />
                </Section>

                <Section
                    description='Modify metadata before a request reaches the origin and before its response reaches the visitor.'
                    icon={Braces}
                    title='Headers'
                >
                    <div className='space-y-7'>
                        <HeaderEditor
                            description='Applied before forwarding the request upstream.'
                            label='Request headers'
                            rules={policy.request_headers}
                            onChange={(request_headers) => onChange({ ...policy, request_headers })}
                        />
                        <div className='border-t border-border pt-6'>
                            <HeaderEditor
                                description='Applied after receiving the origin response.'
                                label='Response headers'
                                rules={policy.response_headers}
                                onChange={(response_headers) =>
                                    onChange({ ...policy, response_headers })
                                }
                            />
                        </div>
                    </div>
                </Section>

                <Section
                    description='Control browser cross-origin access and connection upgrade support.'
                    icon={Globe2}
                    title='CORS and protocols'
                >
                    <div className='space-y-5'>
                        <div className='flex items-center justify-between gap-5 rounded-xl border border-border/70 bg-surface-secondary/20 px-4 py-3.5'>
                            <div>
                                <div className='text-sm font-medium'>
                                    Cross-origin resource sharing
                                </div>
                                <div className='mt-0.5 text-xs leading-5 text-muted'>
                                    Add browser CORS response headers and handle preflight requests
                                    at the edge.
                                </div>
                            </div>
                            <ToggleSwitch
                                isSelected={policy.cors.enabled}
                                label='Enable CORS'
                                onChange={(enabled) =>
                                    onChange({ ...policy, cors: { ...policy.cors, enabled } })
                                }
                            />
                        </div>
                        {policy.cors.enabled && (
                            <div className='grid gap-4 rounded-lg border border-border/70 p-4 sm:grid-cols-2'>
                                <FormField
                                    hint='One origin per line. Use * only when credentials are disabled.'
                                    label='Allowed origins'
                                >
                                    <TextArea
                                        placeholder={
                                            'https://app.example.com\nhttps://admin.example.com'
                                        }
                                        rows={4}
                                        value={policy.cors.allow_origins.join('\n')}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...policy,
                                                cors: {
                                                    ...policy.cors,
                                                    allow_origins: splitLines(event.target.value),
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                                <FormField hint='Comma or line separated.' label='Allowed methods'>
                                    <TextArea
                                        placeholder='GET, HEAD, POST, OPTIONS'
                                        rows={4}
                                        value={policy.cors.allow_methods.join(', ')}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...policy,
                                                cors: {
                                                    ...policy.cors,
                                                    allow_methods: splitLines(
                                                        event.target.value
                                                    ).map((item) => item.toUpperCase()),
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                                <FormField
                                    hint='Leave empty to allow no request headers.'
                                    label='Allowed headers'
                                >
                                    <TextArea
                                        placeholder='Authorization, Content-Type'
                                        rows={3}
                                        value={policy.cors.allow_headers.join(', ')}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...policy,
                                                cors: {
                                                    ...policy.cors,
                                                    allow_headers: splitLines(event.target.value),
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                                <FormField
                                    hint='Headers browser scripts may read.'
                                    label='Exposed headers'
                                >
                                    <TextArea
                                        placeholder='ETag, X-Request-ID'
                                        rows={3}
                                        value={policy.cors.expose_headers.join(', ')}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...policy,
                                                cors: {
                                                    ...policy.cors,
                                                    expose_headers: splitLines(event.target.value),
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                                <FormField
                                    hint='Use 0 to disable preflight caching.'
                                    label='Preflight max age (seconds)'
                                >
                                    <Input
                                        min={0}
                                        type='number'
                                        value={String(policy.cors.max_age_seconds)}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...policy,
                                                cors: {
                                                    ...policy.cors,
                                                    max_age_seconds: Number(event.target.value),
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                                <div className='flex items-end pb-1'>
                                    <div className='flex w-full items-center justify-between gap-4 rounded-lg bg-surface-secondary/30 px-3 py-2.5'>
                                        <span className='text-sm font-medium'>
                                            Allow credentials
                                        </span>
                                        <ToggleSwitch
                                            isSelected={policy.cors.allow_credentials}
                                            label='Allow credentials'
                                            onChange={(allow_credentials) =>
                                                onChange({
                                                    ...policy,
                                                    cors: { ...policy.cors, allow_credentials },
                                                })
                                            }
                                        />
                                    </div>
                                </div>
                            </div>
                        )}
                        <div className='grid gap-3 sm:grid-cols-3'>
                            {[
                                {
                                    key: 'websocket' as const,
                                    label: 'WebSocket',
                                    description: 'Standard WebSocket upgrades',
                                },
                                {
                                    key: 'grpc' as const,
                                    label: 'gRPC',
                                    description: 'gRPC and h2c upstreams',
                                },
                                {
                                    key: 'http_upgrade' as const,
                                    label: 'HTTP Upgrade',
                                    description: 'Other upgrade protocols',
                                },
                            ].map((item) => (
                                <div
                                    className='flex min-h-20 items-center justify-between gap-3 rounded-lg border border-border/70 px-3 py-3'
                                    key={item.key}
                                >
                                    <div>
                                        <div className='text-sm font-medium'>{item.label}</div>
                                        <div className='mt-0.5 text-xs text-muted'>
                                            {item.description}
                                        </div>
                                    </div>
                                    <ToggleSwitch
                                        isSelected={policy.protocols[item.key]}
                                        label={item.label}
                                        onChange={(selected) =>
                                            onChange({
                                                ...policy,
                                                protocols: {
                                                    ...policy.protocols,
                                                    [item.key]: selected,
                                                },
                                            })
                                        }
                                    />
                                </div>
                            ))}
                        </div>
                    </div>
                </Section>

                <Section
                    description='Route selected paths to dedicated upstream groups with independent load balancing.'
                    icon={Network}
                    title='Origin routing'
                >
                    <OriginPoolsEditor
                        pools={policy.origin_pools}
                        onChange={(origin_pools) => onChange({ ...policy, origin_pools })}
                    />
                </Section>

                <Section
                    description='Send matching requests or a stable percentage of traffic to an origin pool.'
                    icon={GitBranch}
                    title='Traffic splits'
                >
                    <SplitsEditor
                        poolNames={poolNames}
                        splits={policy.splits}
                        onChange={(splits) => onChange({ ...policy, splits })}
                    />
                </Section>

                <Section
                    description='Replace selected origin error responses with a controlled response body.'
                    icon={FileWarning}
                    title='Error responses'
                >
                    <ErrorPagesEditor
                        pages={policy.error_pages}
                        onChange={(error_pages) => onChange({ ...policy, error_pages })}
                    />
                </Section>
            </ContentCard>

            <SettingsActionBar
                error={!complete ? 'Complete all added rules before saving.' : undefined}
                isDirty={isDirty}
                isDiscardDisabled={saving}
                onDiscard={onDiscard}
            >
                <Button isDisabled={saving || !complete} onPress={onSave}>
                    <Save className='mr-1.5 h-4 w-4' />
                    {saving ? 'Saving...' : 'Save delivery settings'}
                </Button>
            </SettingsActionBar>
        </div>
    );
}
