import type { CachePolicy, CacheRule } from '@/api';

import { Button, Checkbox, CheckboxGroup, Input, Table } from '@heroui/react';
import {
    ArrowDown,
    ArrowUp,
    Braces,
    Clock3,
    Edit3,
    FileCode2,
    HardDrive,
    Hash,
    KeyRound,
    LockKeyhole,
    Plus,
    Save,
    ShieldCheck,
    Trash2,
    X,
} from 'lucide-react';
import { useRef, useState } from 'react';

import { ByteSizeInput } from '@/components/ByteSizeInput.tsx';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { DurationInput, formatDuration } from '@/components/DurationInput.tsx';
import { FormField } from '@/components/FormField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { SettingsActionBar } from '@/components/SettingsActionBar.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

type Conditions = NonNullable<CacheRule['conditions']>;
type ConditionGroup = NonNullable<Conditions['groups']>[number];
type Condition = NonNullable<ConditionGroup['rules']>[number];
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
const ruleLabels: Record<string, string> = {
    ALL: 'All requests',
    EXTENSION: 'File extension',
    PATH_PREFIX: 'Path prefix',
    PATH_REGEX: 'Path regular expression',
};

function splitValues(value: string) {
    return value
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean);
}

function canonicalHeaderName(value: string) {
    return value
        .trim()
        .toLowerCase()
        .split('-')
        .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
        .join('-');
}

function newCacheRule(index: number): CacheRule {
    return {
        name: `Cache rule ${index + 1}`,
        ttl: { default_seconds: 300, status: {}, client_seconds: 300 },
        conditions: {
            group_operator: 'OR',
            groups: [{ operator: 'OR', rules: [{ type: 'PATH_PREFIX', values: ['/assets/'] }] }],
        },
    };
}

function cloneRule(rule: CacheRule): CacheRule {
    return JSON.parse(JSON.stringify(rule)) as CacheRule;
}

function ruleHasAll(rule: CacheRule) {
    return (rule.conditions?.groups ?? []).some((group) =>
        group.rules?.some((condition) => condition.type === 'ALL')
    );
}

function conditionsValid(conditions: CacheRule['conditions']) {
    const groups = conditions?.groups ?? [];
    const allConditions = groups.flatMap((group) => group.rules ?? []);
    return (
        groups.length >= 1 &&
        groups.length <= 16 &&
        (conditions?.group_operator === 'AND' || conditions?.group_operator === 'OR') &&
        groups.every(
            (group) =>
                (group.operator === 'AND' || group.operator === 'OR') &&
                (group.rules?.length ?? 0) >= 1 &&
                (group.rules?.length ?? 0) <= 32 &&
                (group.rules ?? []).every((condition) => {
                    if (condition.type === 'ALL') {
                        return groups.length === 1 && group.rules?.length === 1;
                    }
                    if (condition.type === 'PATH_REGEX') return Boolean(condition.value?.trim());
                    if (condition.type === 'PATH_PREFIX') {
                        return Boolean(
                            condition.values?.length &&
                                condition.values.every((value) => value.startsWith('/'))
                        );
                    }
                    return condition.type === 'EXTENSION' && Boolean(condition.values?.length);
                })
        ) &&
        (allConditions.every((condition) => condition.type !== 'ALL') || allConditions.length === 1)
    );
}

function cacheRuleValid(rule: CacheRule) {
    const ttl = rule.ttl;
    return (
        Boolean(rule.name?.trim()) &&
        (rule.name?.trim().length ?? 0) <= 80 &&
        (ttl?.default_seconds ?? 0) >= 1 &&
        (ttl?.default_seconds ?? 0) <= maxSeconds &&
        (!ttl?.override_client_ttl ||
            ((ttl.client_seconds ?? -1) >= 0 && (ttl.client_seconds ?? -1) <= maxSeconds)) &&
        Object.entries(ttl?.status ?? {}).every(
            ([status, seconds]) =>
                /^[1-5][0-9]{2}$/.test(status) && seconds >= 1 && seconds <= maxSeconds
        ) &&
        conditionsValid(rule.conditions)
    );
}

function conditionSummary(rule: CacheRule) {
    if (ruleHasAll(rule)) return 'All requests';
    const groups = rule.conditions?.groups ?? [];
    const count = groups.reduce((total, group) => total + (group.rules?.length ?? 0), 0);
    return `${count} condition${count === 1 ? '' : 's'} in ${groups.length} group${groups.length === 1 ? '' : 's'}`;
}

function Section({
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

function SettingToggle({
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

function RuleDialog({
    draft,
    editingIndex,
    existingRules,
    onChange,
    onClose,
    onCommit,
}: {
    draft: CacheRule | null;
    editingIndex: number | null;
    existingRules: CacheRule[];
    onChange: (rule: CacheRule) => void;
    onClose: () => void;
    onCommit: () => void;
}) {
    const groupKeys = useRef<string[]>([]);
    const conditionKeys = useRef<string[][]>([]);
    const statusKeys = useRef<string[]>([]);
    const [valueDrafts, setValueDrafts] = useState<Record<string, string>>({});
    if (!draft) return null;

    const groups = draft.conditions?.groups ?? [];
    const statusEntries = Object.entries(draft.ttl?.status ?? {});
    const duplicateName = existingRules.some(
        (rule, index) =>
            index !== editingIndex &&
            rule.name?.trim().toLowerCase() === draft.name?.trim().toLowerCase()
    );
    const allMustBeLast =
        ruleHasAll(draft) && editingIndex !== null && editingIndex !== existingRules.length - 1;
    const duplicateAll =
        ruleHasAll(draft) &&
        editingIndex === null &&
        existingRules.some((rule) => ruleHasAll(rule));
    const valid = cacheRuleValid(draft) && !duplicateName && !allMustBeLast && !duplicateAll;

    const updateGroups = (next: ConditionGroup[]) =>
        onChange({
            ...draft,
            conditions: {
                ...draft.conditions,
                group_operator: draft.conditions?.group_operator ?? 'OR',
                groups: next,
            },
        });
    const updateCondition = (
        groupIndex: number,
        conditionIndex: number,
        patch: Partial<Condition>
    ) =>
        updateGroups(
            groups.map((group, currentGroup) =>
                currentGroup === groupIndex
                    ? {
                          ...group,
                          rules: (group.rules ?? []).map((condition, currentCondition) =>
                              currentCondition === conditionIndex
                                  ? { ...condition, ...patch }
                                  : condition
                          ),
                      }
                    : group
            )
        );
    const updateStatus = (index: number, status: string, seconds: number) =>
        onChange({
            ...draft,
            ttl: {
                ...draft.ttl,
                default_seconds: draft.ttl?.default_seconds ?? 300,
                status: Object.fromEntries(
                    statusEntries.map((entry, current) =>
                        current === index ? [status, seconds] : entry
                    )
                ),
            },
        });

    return (
        <DialogShell
            icon={<Clock3 className='h-4 w-4' />}
            isOpen
            size='xl'
            subtitle='Configure matching and expiration as one ordered cache rule.'
            title={editingIndex === null ? 'Add cache rule' : 'Edit cache rule'}
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
        >
            <div className='max-h-[72vh] space-y-6 overflow-y-auto px-6 py-5'>
                <FormField
                    htmlFor='cache-rule-name'
                    label='Rule name'
                    error={duplicateName ? 'Rule names must be unique.' : undefined}
                >
                    <Input
                        autoFocus
                        id='cache-rule-name'
                        maxLength={80}
                        value={draft.name ?? ''}
                        variant='secondary'
                        onChange={(event) => onChange({ ...draft, name: event.target.value })}
                    />
                </FormField>

                <div className='border-t border-border pt-5'>
                    <div className='mb-4'>
                        <div className='text-sm font-semibold'>Expiration</div>
                        <div className='mt-0.5 text-xs text-muted'>
                            Edge and browser freshness for responses matched by this rule.
                        </div>
                    </div>
                    <div className='grid gap-4'>
                        <FormField htmlFor='rule-edge-ttl' label='Default edge TTL'>
                            <DurationInput
                                id='rule-edge-ttl'
                                seconds={draft.ttl?.default_seconds ?? 300}
                                minimumSeconds={1}
                                maximumSeconds={maxSeconds}
                                onChange={(default_seconds) =>
                                    onChange({
                                        ...draft,
                                        ttl: {
                                            ...draft.ttl,
                                            default_seconds,
                                            status: draft.ttl?.status ?? {},
                                        },
                                    })
                                }
                            />
                        </FormField>
                        <div className='space-y-3'>
                            <SettingToggle
                                label='Override browser TTL'
                                description='Write a separate client max-age value.'
                                selected={draft.ttl?.override_client_ttl ?? false}
                                onChange={(override_client_ttl) =>
                                    onChange({
                                        ...draft,
                                        ttl: {
                                            ...draft.ttl,
                                            default_seconds: draft.ttl?.default_seconds ?? 300,
                                            status: draft.ttl?.status ?? {},
                                            override_client_ttl,
                                            client_seconds: draft.ttl?.client_seconds ?? 300,
                                        },
                                    })
                                }
                            />
                            {draft.ttl?.override_client_ttl && (
                                <FormField htmlFor='rule-client-ttl' label='Browser TTL'>
                                    <DurationInput
                                        id='rule-client-ttl'
                                        seconds={draft.ttl.client_seconds ?? 300}
                                        minimumSeconds={0}
                                        maximumSeconds={maxSeconds}
                                        onChange={(client_seconds) =>
                                            onChange({
                                                ...draft,
                                                ttl: {
                                                    ...draft.ttl,
                                                    default_seconds:
                                                        draft.ttl?.default_seconds ?? 300,
                                                    status: draft.ttl?.status ?? {},
                                                    override_client_ttl: true,
                                                    client_seconds,
                                                },
                                            })
                                        }
                                    />
                                </FormField>
                            )}
                        </div>
                    </div>
                    <div className='mt-4'>
                        <div className='mb-2 flex items-center justify-between gap-3'>
                            <div className='text-sm font-medium'>Status TTL overrides</div>
                            <Button
                                size='sm'
                                variant='ghost'
                                onPress={() => {
                                    const status = ['200', '301', '302', '404', '500'].find(
                                        (candidate) => !draft.ttl?.status?.[candidate]
                                    );
                                    if (!status) return;
                                    onChange({
                                        ...draft,
                                        ttl: {
                                            ...draft.ttl,
                                            default_seconds: draft.ttl?.default_seconds ?? 300,
                                            status: {
                                                ...draft.ttl?.status,
                                                [status]: draft.ttl?.default_seconds ?? 300,
                                            },
                                        },
                                    });
                                }}
                            >
                                <Plus className='mr-1.5 h-3.5 w-3.5' /> Add status
                            </Button>
                        </div>
                        <div className='space-y-2'>
                            {statusEntries.map(([status, seconds], index) => {
                                statusKeys.current[index] ??= crypto.randomUUID();
                                return (
                                    <div
                                        className='grid grid-cols-[minmax(0,1fr)_36px] gap-2 sm:grid-cols-[110px_minmax(0,1fr)_36px]'
                                        key={statusKeys.current[index]}
                                    >
                                        <Input
                                            aria-label={`Status rule ${index + 1}`}
                                            className='col-span-2 sm:col-span-1'
                                            maxLength={3}
                                            value={status}
                                            variant='secondary'
                                            onChange={(event) =>
                                                updateStatus(index, event.target.value, seconds)
                                            }
                                        />
                                        <DurationInput
                                            id={`status-${statusKeys.current[index]}-ttl`}
                                            seconds={seconds}
                                            minimumSeconds={1}
                                            maximumSeconds={maxSeconds}
                                            onChange={(nextSeconds) =>
                                                updateStatus(index, status, nextSeconds)
                                            }
                                        />
                                        <Button
                                            isIconOnly
                                            aria-label={`Remove status ${status}`}
                                            size='sm'
                                            variant='ghost'
                                            onPress={() => {
                                                const next = { ...draft.ttl?.status };
                                                delete next[status];
                                                onChange({
                                                    ...draft,
                                                    ttl: {
                                                        ...draft.ttl,
                                                        default_seconds:
                                                            draft.ttl?.default_seconds ?? 300,
                                                        status: next,
                                                    },
                                                });
                                            }}
                                        >
                                            <Trash2 className='h-4 w-4 text-danger' />
                                        </Button>
                                    </div>
                                );
                            })}
                        </div>
                    </div>
                </div>

                <div className='border-t border-border pt-5'>
                    <div className='mb-4 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
                        <div>
                            <div className='text-sm font-semibold'>Matching conditions</div>
                            <div className='mt-0.5 text-xs text-muted'>
                                Groups support nested AND/OR matching inside this cache rule.
                            </div>
                        </div>
                        <SelectField
                            ariaLabel='Combine condition groups'
                            className='min-w-36'
                            options={[
                                { id: 'OR', label: 'OR groups' },
                                { id: 'AND', label: 'AND groups' },
                            ]}
                            value={draft.conditions?.group_operator ?? 'OR'}
                            onChange={(group_operator) =>
                                onChange({
                                    ...draft,
                                    conditions: { ...draft.conditions, group_operator, groups },
                                })
                            }
                        />
                    </div>
                    <div className='space-y-3'>
                        {groups.map((group, groupIndex) => {
                            groupKeys.current[groupIndex] ??= crypto.randomUUID();
                            conditionKeys.current[groupIndex] ??= [];
                            return (
                                <div
                                    className='border border-border'
                                    key={groupKeys.current[groupIndex]}
                                >
                                    <div className='flex items-center justify-between gap-3 border-b border-border bg-surface-secondary/30 px-3 py-2'>
                                        <SelectField
                                            ariaLabel={`Condition group ${groupIndex + 1}`}
                                            className='min-w-32'
                                            options={[
                                                { id: 'OR', label: 'OR conditions' },
                                                { id: 'AND', label: 'AND conditions' },
                                            ]}
                                            value={group.operator ?? 'OR'}
                                            onChange={(operator) =>
                                                updateGroups(
                                                    groups.map((item, index) =>
                                                        index === groupIndex
                                                            ? { ...item, operator }
                                                            : item
                                                    )
                                                )
                                            }
                                        />
                                        <Button
                                            isIconOnly
                                            aria-label={`Remove condition group ${groupIndex + 1}`}
                                            isDisabled={groups.length === 1}
                                            size='sm'
                                            variant='ghost'
                                            onPress={() =>
                                                updateGroups(
                                                    groups.filter(
                                                        (_, index) => index !== groupIndex
                                                    )
                                                )
                                            }
                                        >
                                            <Trash2 className='h-4 w-4 text-danger' />
                                        </Button>
                                    </div>
                                    <div className='divide-y divide-border'>
                                        {(group.rules ?? []).map((condition, conditionIndex) => {
                                            conditionKeys.current[groupIndex][conditionIndex] ??=
                                                crypto.randomUUID();
                                            const key =
                                                conditionKeys.current[groupIndex][conditionIndex];
                                            return (
                                                <div
                                                    className='grid gap-2 px-3 py-3 md:grid-cols-[170px_minmax(0,1fr)_36px]'
                                                    key={key}
                                                >
                                                    <SelectField
                                                        ariaLabel={`Condition ${conditionIndex + 1}`}
                                                        options={Object.entries(ruleLabels).map(
                                                            ([id, label]) => ({ id, label })
                                                        )}
                                                        value={condition.type ?? 'PATH_PREFIX'}
                                                        variant='secondary'
                                                        onChange={(type) => {
                                                            if (type === 'ALL') {
                                                                updateGroups([
                                                                    {
                                                                        operator: 'OR',
                                                                        rules: [{ type: 'ALL' }],
                                                                    },
                                                                ]);
                                                                return;
                                                            }
                                                            updateCondition(
                                                                groupIndex,
                                                                conditionIndex,
                                                                { type, value: '', values: [] }
                                                            );
                                                        }}
                                                    />
                                                    {condition.type === 'ALL' ? (
                                                        <div className='flex items-center rounded-lg bg-surface-secondary/50 px-3 text-sm text-muted'>
                                                            Matches every request
                                                        </div>
                                                    ) : condition.type === 'PATH_REGEX' ? (
                                                        <Input
                                                            aria-label='Path regular expression'
                                                            placeholder='^/assets/'
                                                            value={condition.value ?? ''}
                                                            variant='secondary'
                                                            onChange={(event) =>
                                                                updateCondition(
                                                                    groupIndex,
                                                                    conditionIndex,
                                                                    { value: event.target.value }
                                                                )
                                                            }
                                                        />
                                                    ) : (
                                                        <Input
                                                            aria-label='Condition values'
                                                            placeholder={
                                                                condition.type === 'EXTENSION'
                                                                    ? 'css, js, png'
                                                                    : '/assets/, /images/'
                                                            }
                                                            value={
                                                                valueDrafts[key] ??
                                                                (condition.values ?? []).join(', ')
                                                            }
                                                            variant='secondary'
                                                            onBlur={() =>
                                                                setValueDrafts((drafts) => ({
                                                                    ...drafts,
                                                                    [key]: (
                                                                        condition.values ?? []
                                                                    ).join(', '),
                                                                }))
                                                            }
                                                            onChange={(event) => {
                                                                setValueDrafts((drafts) => ({
                                                                    ...drafts,
                                                                    [key]: event.target.value,
                                                                }));
                                                                updateCondition(
                                                                    groupIndex,
                                                                    conditionIndex,
                                                                    {
                                                                        values: splitValues(
                                                                            event.target.value
                                                                        ),
                                                                    }
                                                                );
                                                            }}
                                                        />
                                                    )}
                                                    <Button
                                                        isIconOnly
                                                        aria-label={`Remove condition ${conditionIndex + 1}`}
                                                        isDisabled={
                                                            (group.rules?.length ?? 0) === 1
                                                        }
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() =>
                                                            updateGroups(
                                                                groups.map((item, index) =>
                                                                    index === groupIndex
                                                                        ? {
                                                                              ...item,
                                                                              rules: (
                                                                                  item.rules ?? []
                                                                              ).filter(
                                                                                  (_, current) =>
                                                                                      current !==
                                                                                      conditionIndex
                                                                              ),
                                                                          }
                                                                        : item
                                                                )
                                                            )
                                                        }
                                                    >
                                                        <Trash2 className='h-4 w-4 text-danger' />
                                                    </Button>
                                                </div>
                                            );
                                        })}
                                    </div>
                                    <div className='border-t border-border px-3 py-2'>
                                        <Button
                                            size='sm'
                                            variant='ghost'
                                            onPress={() =>
                                                updateGroups(
                                                    groups.map((item, index) =>
                                                        index === groupIndex
                                                            ? {
                                                                  ...item,
                                                                  rules: [
                                                                      ...(item.rules?.some(
                                                                          (condition) =>
                                                                              condition.type ===
                                                                              'ALL'
                                                                      )
                                                                          ? []
                                                                          : (item.rules ?? [])),
                                                                      {
                                                                          type: 'PATH_PREFIX',
                                                                          values: ['/'],
                                                                      },
                                                                  ],
                                                              }
                                                            : item
                                                    )
                                                )
                                            }
                                        >
                                            <Plus className='mr-1.5 h-3.5 w-3.5' /> Add condition
                                        </Button>
                                    </div>
                                </div>
                            );
                        })}
                    </div>
                    <Button
                        className='mt-3'
                        variant='secondary'
                        onPress={() =>
                            updateGroups([
                                ...(ruleHasAll(draft) ? [] : groups),
                                {
                                    operator: 'OR',
                                    rules: [{ type: 'PATH_PREFIX', values: ['/'] }],
                                },
                            ])
                        }
                    >
                        <Plus className='mr-1.5 h-4 w-4' /> Add condition group
                    </Button>
                    {(!conditionsValid(draft.conditions) || allMustBeLast || duplicateAll) && (
                        <div className='mt-3 rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-xs text-danger'>
                            Conditions must be complete. All requests must be used alone and its
                            cache rule must be last.
                        </div>
                    )}
                </div>
            </div>
            <DialogFooter>
                <Button variant='ghost' onPress={onClose}>
                    Cancel
                </Button>
                <Button isDisabled={!valid} onPress={onCommit}>
                    {editingIndex === null ? 'Add rule' : 'Save rule'}
                </Button>
            </DialogFooter>
        </DialogShell>
    );
}

function EditCacheKeyDialog({
    parts,
    headers,
    onClose,
    onConfirm,
}: {
    parts: CacheKeyPart[];
    headers: string[];
    onClose: () => void;
    onConfirm: (parts: CacheKeyPart[], headers: string[]) => void;
}) {
    const [selectedParts, setSelectedParts] = useState<CacheKeyPart[]>(parts);
    const [selectedHeaders, setSelectedHeaders] = useState<string[]>(headers);
    const [headerDraft, setHeaderDraft] = useState('');
    const normalizedHeader = canonicalHeaderName(headerDraft);
    const headerManaged = normalizedHeader.toLowerCase() === 'accept-encoding';
    const headerValid =
        /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(headerDraft.trim()) &&
        !headerManaged &&
        !selectedHeaders.some((header) => header.toLowerCase() === normalizedHeader.toLowerCase());
    const addHeader = () => {
        if (!headerValid) return;
        setSelectedHeaders([...selectedHeaders, normalizedHeader]);
        setHeaderDraft('');
    };
    const confirm = () => {
        const orderedParts = cacheKeyPartOrder.filter((part) => selectedParts.includes(part));
        onConfirm(orderedParts, selectedHeaders);
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
    const rules = cache.rules ?? [];
    const [editingIndex, setEditingIndex] = useState<number | null>(null);
    const [ruleDraft, setRuleDraft] = useState<CacheRule | null>(null);
    const [addingKeyPart, setAddingKeyPart] = useState(false);
    const [addingBypass, setAddingBypass] = useState(false);
    const methods = cache.methods ?? ['GET', 'HEAD'];
    const cacheKeyParts = cache.cache_key?.parts ?? [];
    const cacheKeyHeaders = cache.cache_key?.headers ?? [];
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
    const cacheKeyPartsValid =
        cacheKeyParts.includes('PATH') &&
        new Set(cacheKeyParts).size === cacheKeyParts.length &&
        cacheKeyParts.every((part) => cacheKeyPartOrder.includes(part));
    const bypassValid =
        new Set(bypassValues.map((value) => value.toLowerCase())).size === bypassValues.length &&
        bypassValues.every((value) => /^[a-z][a-z0-9_-]*(?:=[a-z0-9._-]+)?$/i.test(value));
    const policyValid =
        rules.length <= 32 &&
        rules.every(cacheRuleValid) &&
        rules.every((rule, index) => !ruleHasAll(rule) || index === rules.length - 1) &&
        new Set(rules.map((rule) => rule.name?.trim().toLowerCase())).size === rules.length &&
        methods.length > 0 &&
        maxBodyBytes >= 1 &&
        maxBodyBytes <= maxBodyLimit &&
        staleValid &&
        cacheKeyPartsValid &&
        cacheKeyHeadersValid &&
        bypassValid;

    const setCacheKey = (patch: Partial<NonNullable<CachePolicy['cache_key']>>) =>
        onChange({
            ...cache,
            cache_key: {
                ...cache.cache_key,
                parts: cacheKeyParts,
                headers: cacheKeyHeaders,
                ...patch,
            },
        });
    const confirmCacheKey = (parts: CacheKeyPart[], headers: string[]) => {
        setCacheKey({ parts, headers });
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
    ];
    const cacheKeyPreview = cacheKeyTokens.map((item) => item.token).join(' + ');

    const moveRule = (index: number, offset: number) => {
        const target = index + offset;
        if (target < 0 || target >= rules.length) return;
        const next = [...rules];
        [next[index], next[target]] = [next[target], next[index]];
        onChange({ ...cache, rules: next });
    };
    const openNewRule = () => {
        setEditingIndex(null);
        setRuleDraft(newCacheRule(rules.length));
    };
    const openRule = (index: number) => {
        setEditingIndex(index);
        setRuleDraft(cloneRule(rules[index]));
    };
    const commitRule = () => {
        if (!ruleDraft) return;
        let next: CacheRule[];
        if (editingIndex !== null) {
            next = rules.map((rule, index) => (index === editingIndex ? ruleDraft : rule));
        } else if (rules.length > 0 && ruleHasAll(rules[rules.length - 1])) {
            next = [...rules.slice(0, -1), ruleDraft, rules[rules.length - 1]];
        } else {
            next = [...rules, ruleDraft];
        }
        onChange({ ...cache, rules: next });
        setRuleDraft(null);
    };

    return (
        <>
            <div className='space-y-8'>
                <ContentCard className='overflow-hidden' noPadding>
                    <div className='flex flex-col gap-5 border-b border-border px-5 py-5 sm:flex-row sm:items-center sm:justify-between lg:px-6'>
                        <div className='flex items-center gap-3'>
                            <span
                                className={`flex h-10 w-10 items-center justify-center rounded-lg ${cache.enabled ? 'bg-primary text-primary-foreground' : 'bg-surface-secondary text-muted'}`}
                            >
                                <HardDrive className='h-5 w-5' />
                            </span>
                            <div>
                                <h2 className='text-base font-semibold'>Cache policy</h2>
                                <p className='mt-0.5 text-xs text-muted'>
                                    {cache.enabled
                                        ? `${rules.length} ordered cache rule${rules.length === 1 ? '' : 's'}`
                                        : 'Caching is disabled'}
                                </p>
                            </div>
                        </div>
                        <div className='flex items-center gap-3'>
                            <span className='text-xs font-medium text-muted'>
                                {cache.enabled ? 'Enabled' : 'Disabled'}
                            </span>
                            <ToggleSwitch
                                label='Enable cache'
                                isSelected={cache.enabled ?? false}
                                onChange={(enabled) => onChange({ ...cache, enabled })}
                            />
                        </div>
                    </div>

                    <Section
                        icon={FileCode2}
                        title='Cache rules'
                        description='Rules are evaluated from top to bottom. Each rule owns its matching expression and expiration policy.'
                    >
                        <div className='space-y-3'>
                            <div className='flex justify-end'>
                                <Button onPress={openNewRule}>
                                    <Plus className='mr-1.5 h-4 w-4' /> Add rule
                                </Button>
                            </div>
                            <Table variant='secondary'>
                                <Table.ScrollContainer>
                                    <Table.Content aria-label='Ordered cache rules'>
                                        <Table.Header>
                                            <Table.Column className='w-24'>Order</Table.Column>
                                            <Table.Column isRowHeader>Rule</Table.Column>
                                            <Table.Column>Match</Table.Column>
                                            <Table.Column>TTL</Table.Column>
                                            <Table.Column className='w-24 text-right'>
                                                Actions
                                            </Table.Column>
                                        </Table.Header>
                                        <Table.Body>
                                            {rules.length === 0 ? (
                                                <Table.Row id='empty-cache-rules'>
                                                    <Table.Cell colSpan={5}>
                                                        <div className='py-8 text-center'>
                                                            <div className='text-sm font-medium'>
                                                                No cache rules
                                                            </div>
                                                            <div className='mt-1 text-xs text-muted'>
                                                                Requests bypass cache until you add
                                                                a rule.
                                                            </div>
                                                        </div>
                                                    </Table.Cell>
                                                </Table.Row>
                                            ) : (
                                                rules.map((rule, index) => (
                                                    <Table.Row
                                                        id={`${rule.name}-${index}`}
                                                        key={rule.name}
                                                    >
                                                        <Table.Cell>
                                                            <div className='flex items-center gap-1'>
                                                                <span className='w-6 font-mono text-xs text-muted'>
                                                                    {String(index + 1).padStart(
                                                                        2,
                                                                        '0'
                                                                    )}
                                                                </span>
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label={`Move ${rule.name} up`}
                                                                    isDisabled={
                                                                        index === 0 ||
                                                                        ruleHasAll(rule)
                                                                    }
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() =>
                                                                        moveRule(index, -1)
                                                                    }
                                                                >
                                                                    <ArrowUp className='h-3.5 w-3.5' />
                                                                </Button>
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label={`Move ${rule.name} down`}
                                                                    isDisabled={
                                                                        index ===
                                                                            rules.length - 1 ||
                                                                        ruleHasAll(rule) ||
                                                                        ruleHasAll(rules[index + 1])
                                                                    }
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() =>
                                                                        moveRule(index, 1)
                                                                    }
                                                                >
                                                                    <ArrowDown className='h-3.5 w-3.5' />
                                                                </Button>
                                                            </div>
                                                        </Table.Cell>
                                                        <Table.Cell>
                                                            <span className='font-semibold'>
                                                                {rule.name}
                                                            </span>
                                                        </Table.Cell>
                                                        <Table.Cell>
                                                            <span className='whitespace-nowrap text-xs text-muted'>
                                                                {conditionSummary(rule)}
                                                            </span>
                                                        </Table.Cell>
                                                        <Table.Cell>
                                                            <div className='flex items-center gap-2 whitespace-nowrap text-xs'>
                                                                <Clock3 className='h-3.5 w-3.5 text-muted' />
                                                                <span className='font-medium'>
                                                                    {formatDuration(
                                                                        rule.ttl?.default_seconds ??
                                                                            0
                                                                    )}{' '}
                                                                    edge
                                                                </span>
                                                                {rule.ttl?.override_client_ttl && (
                                                                    <span className='text-muted'>
                                                                        /{' '}
                                                                        {formatDuration(
                                                                            rule.ttl
                                                                                .client_seconds ?? 0
                                                                        )}{' '}
                                                                        browser
                                                                    </span>
                                                                )}
                                                            </div>
                                                        </Table.Cell>
                                                        <Table.Cell>
                                                            <div className='flex items-center justify-end gap-1'>
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label={`Edit ${rule.name}`}
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() => openRule(index)}
                                                                >
                                                                    <Edit3 className='h-4 w-4' />
                                                                </Button>
                                                                <Button
                                                                    isIconOnly
                                                                    aria-label={`Delete ${rule.name}`}
                                                                    size='sm'
                                                                    variant='ghost'
                                                                    onPress={() =>
                                                                        onChange({
                                                                            ...cache,
                                                                            rules: rules.filter(
                                                                                (_, current) =>
                                                                                    current !==
                                                                                    index
                                                                            ),
                                                                        })
                                                                    }
                                                                >
                                                                    <Trash2 className='h-4 w-4 text-danger' />
                                                                </Button>
                                                            </div>
                                                        </Table.Cell>
                                                    </Table.Row>
                                                ))
                                            )}
                                        </Table.Body>
                                    </Table.Content>
                                </Table.ScrollContainer>
                            </Table>
                            {!policyValid && (
                                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-xs text-danger'>
                                    Resolve invalid or duplicate rules before saving. An All
                                    requests rule must remain last.
                                </div>
                            )}
                        </div>
                    </Section>

                    <Section
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
                                            variant={
                                                keyStorageMode === option.id ? 'primary' : 'ghost'
                                            }
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
                                            Every selected value must match before an object is
                                            reused.
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
                                                <span
                                                    aria-hidden='true'
                                                    className='text-sm text-muted'
                                                >
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
                                </div>
                                {(!cacheKeyPartsValid || !cacheKeyHeadersValid) && (
                                    <p className='mt-2 text-xs text-danger'>
                                        The key must contain URI path and use unique, valid header
                                        names.
                                    </p>
                                )}
                            </div>
                        </div>
                    </Section>

                    <Section
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
                                                            bypass_cache_control:
                                                                bypassValues.filter(
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
                    </Section>

                    <Section
                        icon={ShieldCheck}
                        title='Cache behavior'
                        description='Set cacheable methods, object limits, stale delivery, and response diagnostics.'
                    >
                        <div className='space-y-4'>
                            <fieldset
                                aria-label='Cacheable methods'
                                className='flex flex-wrap gap-2'
                            >
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
                                                    ? methods.filter(
                                                          (current) => current !== method
                                                      )
                                                    : [...methods, method];
                                                onChange({
                                                    ...cache,
                                                    methods: next,
                                                    request_coalescing:
                                                        cache.request_coalescing !== false &&
                                                        next.every((current) =>
                                                            ['GET', 'HEAD', 'OPTIONS'].includes(
                                                                current
                                                            )
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
                    </Section>
                </ContentCard>

                <SettingsActionBar
                    isDirty={isDirty}
                    isDiscardDisabled={saving}
                    onDiscard={onDiscard}
                >
                    <Button isDisabled={saving || !policyValid} onPress={onSave}>
                        <Save className='mr-1.5 h-4 w-4' />
                        {saving ? 'Saving...' : 'Save cache policy'}
                    </Button>
                </SettingsActionBar>
            </div>

            <RuleDialog
                draft={ruleDraft}
                editingIndex={editingIndex}
                existingRules={rules}
                onChange={setRuleDraft}
                onClose={() => setRuleDraft(null)}
                onCommit={commitRule}
            />
            {addingKeyPart && (
                <EditCacheKeyDialog
                    headers={cacheKeyHeaders}
                    parts={cacheKeyParts}
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
