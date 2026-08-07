import type { CachePolicy, CacheRule } from '@/api';

import { Button, Input, Table } from '@heroui/react';
import {
    ArrowDown,
    ArrowUp,
    Clock3,
    Edit3,
    FileCode2,
    HardDrive,
    Plus,
    Save,
    Trash2,
} from 'lucide-react';
import { useRef, useState } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { DurationInput, formatDuration } from '@/components/DurationInput.tsx';
import { FormField } from '@/components/FormField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { SettingsActionBar } from '@/components/SettingsActionBar.tsx';
import { Section, SettingToggle } from '@/components/SiteCacheSettings.tsx';

type Conditions = NonNullable<CacheRule['conditions']>;
type ConditionGroup = NonNullable<Conditions['groups']>[number];
type Condition = NonNullable<ConditionGroup['rules']>[number];

interface SiteCacheRulesProps {
    cache: CachePolicy;
    isDirty: boolean;
    saving: boolean;
    onChange: (cache: CachePolicy) => void;
    onDiscard: () => void;
    onSave: () => void;
}

const maxSeconds = 31_536_000;
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

export function SiteCacheRules({
    cache,
    isDirty,
    saving,
    onChange,
    onDiscard,
    onSave,
}: SiteCacheRulesProps) {
    const rules = cache.rules ?? [];
    const [editingIndex, setEditingIndex] = useState<number | null>(null);
    const [ruleDraft, setRuleDraft] = useState<CacheRule | null>(null);
    const rulesValid =
        rules.length <= 32 &&
        rules.every(cacheRuleValid) &&
        rules.every((rule, index) => !ruleHasAll(rule) || index === rules.length - 1) &&
        new Set(rules.map((rule) => rule.name?.trim().toLowerCase())).size === rules.length;

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
            <div className='space-y-4'>
                <ContentCard className='overflow-hidden' noPadding>
                    <div className='flex flex-col gap-5 border-b border-border px-5 py-5 sm:flex-row sm:items-center sm:justify-between lg:px-6'>
                        <div className='flex items-center gap-3'>
                            <span className='flex h-10 w-10 items-center justify-center rounded-lg bg-primary text-primary-foreground'>
                                <HardDrive className='h-5 w-5' />
                            </span>
                            <div>
                                <h2 className='text-base font-semibold'>Cache rules</h2>
                                <p className='mt-0.5 text-xs text-muted'>
                                    {`${rules.length} ordered cache rule${rules.length === 1 ? '' : 's'}`}
                                </p>
                            </div>
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
                            {!rulesValid && (
                                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-xs text-danger'>
                                    Resolve invalid or duplicate rules before saving. An All
                                    requests rule must remain last.
                                </div>
                            )}
                        </div>
                    </Section>
                </ContentCard>

                <SettingsActionBar
                    isDirty={isDirty}
                    isDiscardDisabled={saving}
                    onDiscard={onDiscard}
                >
                    <Button isDisabled={saving || !rulesValid} onPress={onSave}>
                        <Save className='mr-1.5 h-4 w-4' />
                        {saving ? 'Saving...' : 'Save cache rules'}
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
        </>
    );
}
