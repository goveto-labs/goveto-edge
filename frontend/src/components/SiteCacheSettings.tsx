import type { CachePolicy } from '@/api';

import { Button, Input } from '@heroui/react';
import { Braces, FileCode2, HardDrive, Plus, Save, ShieldCheck, Trash2 } from 'lucide-react';
import { useRef, useState } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { FormField } from '@/components/FormField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

type ConditionGroup = NonNullable<NonNullable<CachePolicy['conditions']>['groups']>[number];
type ConditionRule = NonNullable<ConditionGroup['rules']>[number];

interface SiteCacheSettingsProps {
    cache: CachePolicy;
    saving: boolean;
    onChange: (cache: CachePolicy) => void;
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

function Section({
    className = '',
    icon: Icon,
    title,
    description,
    children,
}: {
    className?: string;
    icon: typeof HardDrive;
    title: string;
    description: string;
    children: React.ReactNode;
}) {
    return (
        <section
            className={`grid gap-5 border-t border-border px-5 py-6 lg:grid-cols-[220px_minmax(0,1fr)] lg:px-6 ${className}`}
        >
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
        <div className='flex items-center justify-between gap-5 rounded-xl border border-border/70 bg-surface-secondary/20 px-4 py-3.5'>
            <div>
                <div className='text-sm font-medium'>{label}</div>
                <div className='mt-0.5 text-xs leading-5 text-muted'>{description}</div>
            </div>
            <ToggleSwitch label={label} isSelected={selected} onChange={onChange} />
        </div>
    );
}

function cacheSummary(cache: CachePolicy) {
    if (!cache.enabled) return 'Caching is disabled';
    const groups = cache.conditions?.groups?.length ?? 0;
    return `${groups} condition ${groups === 1 ? 'group' : 'groups'}`;
}

export function SiteCacheSettings({ cache, saving, onChange, onSave }: SiteCacheSettingsProps) {
    const groupKeys = useRef<string[]>([]);
    const ruleKeys = useRef<string[][]>([]);
    const [ruleValueDrafts, setRuleValueDrafts] = useState<Record<string, string>>({});
    const groupKeyAt = (index: number) => {
        groupKeys.current[index] ??= crypto.randomUUID();
        return groupKeys.current[index];
    };
    const ruleKeyAt = (groupIndex: number, ruleIndex: number) => {
        ruleKeys.current[groupIndex] ??= [];
        ruleKeys.current[groupIndex][ruleIndex] ??= crypto.randomUUID();
        return ruleKeys.current[groupIndex][ruleIndex];
    };
    const groups = cache.conditions?.groups ?? [{ operator: 'OR', rules: [{ type: 'ALL' }] }];
    const updateGroups = (nextGroups: ConditionGroup[]) =>
        onChange({
            ...cache,
            conditions: {
                ...cache.conditions,
                group_operator: cache.conditions?.group_operator ?? 'OR',
                groups: nextGroups,
            },
        });
    const updateRule = (groupIndex: number, ruleIndex: number, patch: Partial<ConditionRule>) =>
        updateGroups(
            groups.map((group, currentGroup) =>
                currentGroup === groupIndex
                    ? {
                          ...group,
                          rules: (group.rules ?? []).map((rule, currentRule) =>
                              currentRule === ruleIndex ? { ...rule, ...patch } : rule
                          ),
                      }
                    : group
            )
        );
    const staleValid =
        !cache.stale?.enabled ||
        ((cache.stale.if_error_seconds ?? 0) >= 1 &&
            (cache.stale.if_error_seconds ?? 0) <= maxSeconds);
    const allRules = groups.flatMap((group) => group.rules ?? []);
    const conditionsValid =
        groups.length >= 1 &&
        groups.length <= 16 &&
        groups.every(
            (group) =>
                (group.operator === 'AND' || group.operator === 'OR') &&
                (group.rules?.length ?? 0) >= 1 &&
                (group.rules?.length ?? 0) <= 32 &&
                (group.rules ?? []).every((rule) => {
                    if (rule.type === 'ALL') {
                        return groups.length === 1 && group.rules?.length === 1;
                    }
                    if (rule.type === 'PATH_REGEX') return Boolean(rule.value?.trim());
                    if (rule.type === 'PATH_PREFIX') {
                        return Boolean(
                            rule.values?.length &&
                                rule.values.every((value) => value.startsWith('/'))
                        );
                    }
                    if (rule.type === 'EXTENSION') return Boolean(rule.values?.length);
                    return false;
                })
        ) &&
        (allRules.every((rule) => rule.type !== 'ALL') || allRules.length === 1);
    const varyHeaders = cache.vary_headers ?? [];
    const varyHeadersValid =
        new Set(varyHeaders.map((header) => header.toLowerCase())).size === varyHeaders.length;

    return (
        <ContentCard className='overflow-hidden' noPadding>
            <div className='flex flex-col gap-5 border-b border-border px-5 py-5 sm:flex-row sm:items-center sm:justify-between lg:px-6'>
                <div>
                    <div className='flex items-center gap-3'>
                        <span
                            className={`flex h-10 w-10 items-center justify-center rounded-xl ${cache.enabled ? 'bg-primary text-primary-foreground' : 'bg-surface-secondary text-muted'}`}
                        >
                            <HardDrive className='h-5 w-5' />
                        </span>
                        <div>
                            <h2 className='text-base font-semibold'>Cache policy</h2>
                            <p className='mt-0.5 text-xs text-muted'>{cacheSummary(cache)}</p>
                        </div>
                    </div>
                </div>
                <div className='flex items-center gap-3'>
                    <span
                        className={`text-xs font-medium ${cache.enabled ? 'text-success' : 'text-muted'}`}
                    >
                        {cache.enabled ? 'Enabled' : 'Disabled'}
                    </span>
                    <ToggleSwitch
                        label='Enable cache'
                        isSelected={cache.enabled ?? false}
                        onChange={(enabled) => onChange({ ...cache, enabled })}
                    />
                </div>
            </div>

            <div className='flex flex-col'>
                <div className='order-2 border-t border-border'>
                    <div className='px-5 py-4 lg:px-6'>
                        <div>
                            <div className='text-sm font-semibold'>Advanced settings</div>
                            <div className='mt-0.5 text-xs text-muted'>
                                Cache key, response headers, and origin failure behavior.
                            </div>
                        </div>
                    </div>
                    <div className='flex flex-col'>
                        <Section
                            className='order-2'
                            icon={ShieldCheck}
                            title='Origin failure'
                            description='Serve an expired cached response when the origin is temporarily unavailable.'
                        >
                            <div className='space-y-4'>
                                <SettingToggle
                                    label='Serve stale on origin error'
                                    description='Reduces visible outages while the origin recovers.'
                                    selected={cache.stale?.enabled ?? true}
                                    onChange={(enabled) =>
                                        onChange({
                                            ...cache,
                                            stale: {
                                                ...cache.stale,
                                                enabled,
                                                if_error_seconds:
                                                    cache.stale?.if_error_seconds ?? 86400,
                                            },
                                        })
                                    }
                                />
                                {cache.stale?.enabled && (
                                    <FormField
                                        htmlFor='cache-stale-time'
                                        label='Stale availability window'
                                        hint='How long an expired response remains eligible during origin errors.'
                                        error={
                                            staleValid
                                                ? undefined
                                                : 'Use a value between 1 and 31536000 seconds.'
                                        }
                                    >
                                        <div className='relative max-w-sm'>
                                            <Input
                                                id='cache-stale-time'
                                                min={1}
                                                max={maxSeconds}
                                                type='number'
                                                value={String(
                                                    cache.stale.if_error_seconds ?? 86400
                                                )}
                                                variant='secondary'
                                                onChange={(event) =>
                                                    onChange({
                                                        ...cache,
                                                        stale: {
                                                            ...cache.stale,
                                                            enabled: true,
                                                            if_error_seconds: Number(
                                                                event.target.value
                                                            ),
                                                        },
                                                    })
                                                }
                                            />
                                            <span className='pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-xs text-muted'>
                                                seconds
                                            </span>
                                        </div>
                                    </FormField>
                                )}
                            </div>
                        </Section>

                        <Section
                            className='order-1'
                            icon={Braces}
                            title='Cache key and headers'
                            description='Choose which request headers vary the cache and which diagnostic headers are returned.'
                        >
                            <div className='space-y-4'>
                                <FormField
                                    htmlFor='cache-vary-headers'
                                    label='Vary headers'
                                    hint='Comma-separated request header names. Header names are normalized when saved.'
                                >
                                    <Input
                                        id='cache-vary-headers'
                                        placeholder='Accept-Encoding, Accept-Language'
                                        value={(cache.vary_headers ?? []).join(', ')}
                                        variant='secondary'
                                        onChange={(event) =>
                                            onChange({
                                                ...cache,
                                                vary_headers: splitValues(event.target.value),
                                            })
                                        }
                                    />
                                </FormField>
                                <FormField
                                    htmlFor='cache-surrogate-header'
                                    label='Surrogate key header'
                                    hint='Currently fixed to Surrogate-Key by the Agent cache implementation.'
                                >
                                    <Input
                                        id='cache-surrogate-header'
                                        disabled
                                        value={cache.surrogate_key_header ?? 'Surrogate-Key'}
                                        variant='secondary'
                                    />
                                </FormField>
                                <div className='grid gap-3 md:grid-cols-2'>
                                    <SettingToggle
                                        label='Expose X-Cache'
                                        description='Show HIT, MISS, and BYPASS decisions in responses.'
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
                                        description='Return the current cached response age in seconds.'
                                        selected={cache.response_headers?.age ?? true}
                                        onChange={(age) =>
                                            onChange({
                                                ...cache,
                                                response_headers: {
                                                    ...cache.response_headers,
                                                    x_cache:
                                                        cache.response_headers?.x_cache ?? true,
                                                    age,
                                                },
                                            })
                                        }
                                    />
                                    <SettingToggle
                                        label='Allow PURGE method'
                                        description='Permit authenticated cache invalidation through PURGE.'
                                        selected={cache.allow_purge_method ?? false}
                                        onChange={(allow_purge_method) =>
                                            onChange({ ...cache, allow_purge_method })
                                        }
                                    />
                                </div>
                            </div>
                        </Section>
                    </div>
                </div>

                <Section
                    className='order-1'
                    icon={FileCode2}
                    title='Cache conditions'
                    description='Define which requests are eligible. Groups are combined first, then rules inside each group.'
                >
                    <div className='space-y-4'>
                        <div className='flex flex-col gap-3 rounded-xl border border-border/70 bg-surface-secondary/20 px-4 py-3 sm:flex-row sm:items-center sm:justify-between'>
                            <div>
                                <div className='text-sm font-medium'>Combine groups using</div>
                                <div className='mt-0.5 text-xs text-muted'>
                                    OR matches any group. AND requires every group.
                                </div>
                            </div>
                            <SelectField
                                ariaLabel='Cache condition group operator'
                                className='min-w-24'
                                options={[
                                    { id: 'OR', label: 'OR' },
                                    { id: 'AND', label: 'AND' },
                                ]}
                                value={cache.conditions?.group_operator ?? 'OR'}
                                onChange={(value) =>
                                    onChange({
                                        ...cache,
                                        conditions: {
                                            ...cache.conditions,
                                            group_operator: value,
                                            groups,
                                        },
                                    })
                                }
                            />
                        </div>
                        <div className='space-y-3'>
                            {groups.map((group, groupIndex) => (
                                <div
                                    className='rounded-xl border border-border/70'
                                    key={groupKeyAt(groupIndex)}
                                >
                                    <div className='flex items-center justify-between gap-3 border-b border-border bg-surface-secondary/25 px-4 py-3'>
                                        <div className='flex items-center gap-3'>
                                            <span className='font-mono text-xs text-muted'>
                                                Group {groupIndex + 1}
                                            </span>
                                            <SelectField
                                                ariaLabel={`Group ${groupIndex + 1} operator`}
                                                className='min-w-28'
                                                options={[
                                                    { id: 'OR', label: 'OR rules' },
                                                    { id: 'AND', label: 'AND rules' },
                                                ]}
                                                value={group.operator ?? 'OR'}
                                                onChange={(value) =>
                                                    updateGroups(
                                                        groups.map((item, index) =>
                                                            index === groupIndex
                                                                ? {
                                                                      ...item,
                                                                      operator: value,
                                                                  }
                                                                : item
                                                        )
                                                    )
                                                }
                                            />
                                        </div>
                                        <Button
                                            isIconOnly
                                            aria-label={`Remove group ${groupIndex + 1}`}
                                            size='sm'
                                            variant='ghost'
                                            onPress={() =>
                                                updateGroups(
                                                    groups.length === 1
                                                        ? [
                                                              {
                                                                  operator: 'OR',
                                                                  rules: [{ type: 'ALL' }],
                                                              },
                                                          ]
                                                        : groups.filter(
                                                              (_, index) => index !== groupIndex
                                                          )
                                                )
                                            }
                                        >
                                            <Trash2 className='h-4 w-4 text-danger' />
                                        </Button>
                                    </div>
                                    <div className='divide-y divide-border'>
                                        {(group.rules ?? []).map((rule, ruleIndex) => (
                                            <div
                                                className='grid gap-3 px-4 py-3 md:grid-cols-[190px_minmax(0,1fr)_auto]'
                                                key={ruleKeyAt(groupIndex, ruleIndex)}
                                            >
                                                <SelectField
                                                    ariaLabel={`Rule ${ruleIndex + 1} type`}
                                                    options={Object.entries(ruleLabels).map(
                                                        ([value, label]) => ({ id: value, label })
                                                    )}
                                                    value={rule.type ?? 'ALL'}
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
                                                        updateRule(groupIndex, ruleIndex, {
                                                            type,
                                                            value: '',
                                                            values: [],
                                                        });
                                                    }}
                                                />
                                                {rule.type === 'ALL' ? (
                                                    <div className='flex items-center rounded-lg bg-surface-secondary/40 px-3 text-sm text-muted'>
                                                        Every request is cacheable.
                                                    </div>
                                                ) : rule.type === 'PATH_REGEX' ? (
                                                    <Input
                                                        aria-label='Path regular expression'
                                                        placeholder='^/assets/'
                                                        value={rule.value ?? ''}
                                                        variant='secondary'
                                                        onChange={(event) =>
                                                            updateRule(groupIndex, ruleIndex, {
                                                                value: event.target.value,
                                                            })
                                                        }
                                                    />
                                                ) : (
                                                    <Input
                                                        aria-label={`${ruleLabels[rule.type ?? '']} values`}
                                                        placeholder={
                                                            rule.type === 'EXTENSION'
                                                                ? 'css, js, png'
                                                                : '/assets/, /images/'
                                                        }
                                                        value={
                                                            ruleValueDrafts[
                                                                ruleKeyAt(groupIndex, ruleIndex)
                                                            ] ?? (rule.values ?? []).join(', ')
                                                        }
                                                        variant='secondary'
                                                        onBlur={() => {
                                                            const key = ruleKeyAt(
                                                                groupIndex,
                                                                ruleIndex
                                                            );
                                                            setRuleValueDrafts((drafts) => {
                                                                if (!(key in drafts)) return drafts;
                                                                return {
                                                                    ...drafts,
                                                                    [key]: splitValues(
                                                                        drafts[key]
                                                                    ).join(', '),
                                                                };
                                                            });
                                                        }}
                                                        onChange={(event) => {
                                                            const key = ruleKeyAt(
                                                                groupIndex,
                                                                ruleIndex
                                                            );
                                                            setRuleValueDrafts((drafts) => ({
                                                                ...drafts,
                                                                [key]: event.target.value,
                                                            }));
                                                            updateRule(groupIndex, ruleIndex, {
                                                                values: splitValues(
                                                                    event.target.value
                                                                ),
                                                            });
                                                        }}
                                                    />
                                                )}
                                                <Button
                                                    isIconOnly
                                                    aria-label={`Remove rule ${ruleIndex + 1}`}
                                                    isDisabled={(group.rules?.length ?? 0) <= 1}
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
                                                                              (_, currentRule) =>
                                                                                  currentRule !==
                                                                                  ruleIndex
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
                                        ))}
                                    </div>
                                    <div className='border-t border-border px-4 py-3'>
                                        <Button
                                            size='sm'
                                            variant='ghost'
                                            onPress={() =>
                                                updateGroups(
                                                    groups.map((item, index) =>
                                                        index === groupIndex
                                                            ? {
                                                                  ...item,
                                                                  rules: item.rules?.some(
                                                                      (rule) => rule.type === 'ALL'
                                                                  )
                                                                      ? [
                                                                            {
                                                                                type: 'EXTENSION',
                                                                                values: [
                                                                                    'css',
                                                                                    'js',
                                                                                ],
                                                                            },
                                                                        ]
                                                                      : [
                                                                            ...(item.rules ?? []),
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
                                            <Plus className='mr-1.5 h-3.5 w-3.5' /> Add rule
                                        </Button>
                                    </div>
                                </div>
                            ))}
                        </div>
                        <Button
                            variant='secondary'
                            onPress={() =>
                                updateGroups(
                                    groups.some((group) =>
                                        group.rules?.some((rule) => rule.type === 'ALL')
                                    )
                                        ? [
                                              {
                                                  operator: 'OR',
                                                  rules: [{ type: 'PATH_PREFIX', values: ['/'] }],
                                              },
                                              {
                                                  operator: 'OR',
                                                  rules: [
                                                      { type: 'EXTENSION', values: ['css', 'js'] },
                                                  ],
                                              },
                                          ]
                                        : [
                                              ...groups,
                                              {
                                                  operator: 'OR',
                                                  rules: [{ type: 'PATH_PREFIX', values: ['/'] }],
                                              },
                                          ]
                                )
                            }
                        >
                            <Plus className='mr-1.5 h-4 w-4' /> Add condition group
                        </Button>
                        {!conditionsValid && (
                            <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-xs leading-5 text-danger'>
                                Every group needs a valid rule. Path prefixes must start with /,
                                regular expressions cannot be empty, and ALL must be used by itself.
                            </div>
                        )}
                        {!varyHeadersValid && (
                            <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-xs leading-5 text-danger'>
                                Vary headers cannot contain duplicate names.
                            </div>
                        )}
                    </div>
                </Section>

                <div className='order-3 flex flex-col gap-3 border-t border-border bg-surface px-5 py-4 sm:flex-row sm:items-center sm:justify-between lg:px-6'>
                    <div className='text-xs text-muted'>
                        Saving publishes a new site configuration to online nodes.
                    </div>
                    <Button
                        isDisabled={saving || !staleValid || !conditionsValid || !varyHeadersValid}
                        onPress={onSave}
                    >
                        <Save className='mr-1.5 h-4 w-4' />{' '}
                        {saving ? 'Saving...' : 'Save cache policy'}
                    </Button>
                </div>
            </div>
        </ContentCard>
    );
}
