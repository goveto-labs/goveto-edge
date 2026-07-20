import type {
    RateLimitRule,
    RequestConditionGroup,
    SecurityPolicy,
    WAFRequestRule,
    WAFRuleGroup,
} from '@/api';

import { Button, Input } from '@heroui/react';
import { Plus, Save, ShieldCheck, Trash2, Zap } from 'lucide-react';
import { useMemo } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

const presets = [
    ['SQL_INJECTION', 'SQL injection'],
    ['XSS', 'Cross-site scripting'],
    ['PATH_TRAVERSAL', 'Path traversal'],
    ['COMMAND_INJECTION', 'Command injection'],
    ['SCANNER', 'Sensitive path scanners'],
    ['BAD_BOTS', 'Known attack tools'],
] as const;

const fields = [
    ['METHOD', 'Method'],
    ['HOST', 'Host'],
    ['PATH', 'Path'],
    ['RAW_QUERY', 'Raw query'],
    ['QUERY', 'Query parameter'],
    ['HEADER', 'Header'],
    ['COOKIE', 'Cookie'],
    ['BODY', 'Request body'],
    ['CLIENT_IP', 'Client IP'],
    ['USER_AGENT', 'User agent'],
] as const;

const operators = [
    ['EXISTS', 'Exists'],
    ['EQUALS', 'Equals'],
    ['CONTAINS', 'Contains'],
    ['PREFIX', 'Starts with'],
    ['SUFFIX', 'Ends with'],
    ['REGEX', 'Regular expression'],
    ['IN', 'In list'],
    ['CIDR', 'In CIDR list'],
] as const;

function newRule(): WAFRequestRule {
    return { id: crypto.randomUUID(), field: 'PATH', operator: 'PREFIX', value: '/' };
}

function newWAFGroup(): WAFRuleGroup {
    return {
        id: crypto.randomUUID(),
        name: 'Custom rule group',
        enabled: true,
        operator: 'AND',
        action: 'BLOCK',
        status_code: 403,
        rules: [newRule()],
    };
}

function newConditionGroup(): RequestConditionGroup {
    return { id: crypto.randomUUID(), operator: 'AND', rules: [newRule()] };
}

function newRateLimitRule(): RateLimitRule {
    return {
        id: crypto.randomUUID(),
        name: 'CC protection',
        enabled: true,
        key: 'CLIENT_IP_PATH',
        requests: 60,
        window_seconds: 60,
        burst: 20,
        ban_seconds: 300,
        status_code: 429,
        conditions: { group_operator: 'AND', groups: [] },
    };
}

function splitValues(value: string) {
    return value
        .split(',')
        .map((item) => item.trim())
        .filter(Boolean);
}

function LabeledInput({
    label,
    value,
    type = 'text',
    min,
    max,
    onChange,
}: {
    label: string;
    value: string;
    type?: 'text' | 'number';
    min?: number;
    max?: number;
    onChange: (value: string) => void;
}) {
    return (
        <div className='flex flex-col gap-1.5 text-sm font-medium'>
            <span>{label}</span>
            <Input
                aria-label={label}
                max={max}
                min={min}
                type={type}
                value={value}
                variant='secondary'
                onChange={(event) => onChange(event.target.value)}
            />
        </div>
    );
}

function RuleRow({
    rule,
    onChange,
    onRemove,
    removeDisabled,
}: {
    rule: WAFRequestRule;
    onChange: (rule: WAFRequestRule) => void;
    onRemove: () => void;
    removeDisabled: boolean;
}) {
    const needsName = ['QUERY', 'HEADER', 'COOKIE'].includes(rule.field);
    const needsValues = rule.operator === 'IN' || rule.operator === 'CIDR';
    const needsValue = rule.operator !== 'EXISTS' && !needsValues;
    return (
        <div className='grid gap-2 border-t border-border px-4 py-3 first:border-t-0 lg:grid-cols-[150px_150px_minmax(140px,0.7fr)_minmax(220px,1fr)_auto]'>
            <select
                aria-label='Request field'
                className='rounded-lg border border-border bg-surface-secondary px-3 py-2 text-sm'
                value={rule.field}
                onChange={(event) => {
                    const field = event.target.value;
                    onChange({
                        ...rule,
                        field,
                        name: ['QUERY', 'HEADER', 'COOKIE'].includes(field) ? rule.name : undefined,
                        operator:
                            field === 'CLIENT_IP'
                                ? rule.operator
                                : rule.operator === 'CIDR'
                                  ? 'EQUALS'
                                  : rule.operator,
                    });
                }}
            >
                {fields.map(([value, label]) => (
                    <option key={value} value={value}>
                        {label}
                    </option>
                ))}
            </select>
            <select
                aria-label='Match operator'
                className='rounded-lg border border-border bg-surface-secondary px-3 py-2 text-sm'
                value={rule.operator}
                onChange={(event) =>
                    onChange({
                        ...rule,
                        operator: event.target.value,
                        value: event.target.value === 'EXISTS' ? undefined : rule.value,
                        values: ['IN', 'CIDR'].includes(event.target.value)
                            ? rule.values
                            : undefined,
                    })
                }
            >
                {operators
                    .filter(([value]) => value !== 'CIDR' || rule.field === 'CLIENT_IP')
                    .map(([value, label]) => (
                        <option key={value} value={value}>
                            {label}
                        </option>
                    ))}
            </select>
            {needsName ? (
                <Input
                    aria-label='Field name'
                    placeholder={
                        rule.field === 'QUERY'
                            ? 'token'
                            : rule.field === 'HEADER'
                              ? 'X-Token'
                              : 'session'
                    }
                    value={rule.name ?? ''}
                    variant='secondary'
                    onChange={(event) => onChange({ ...rule, name: event.target.value })}
                />
            ) : (
                <div className='flex items-center rounded-lg bg-surface-secondary/35 px-3 text-xs text-muted'>
                    Entire field
                </div>
            )}
            {needsValues ? (
                <Input
                    aria-label='Match values'
                    placeholder={
                        rule.operator === 'CIDR' ? '192.0.2.0/24, 2001:db8::/32' : 'GET, HEAD'
                    }
                    value={(rule.values ?? []).join(', ')}
                    variant='secondary'
                    onChange={(event) =>
                        onChange({ ...rule, values: splitValues(event.target.value) })
                    }
                />
            ) : needsValue ? (
                <Input
                    aria-label='Match value'
                    placeholder={rule.operator === 'REGEX' ? '^/api/' : '/admin'}
                    value={rule.value ?? ''}
                    variant='secondary'
                    onChange={(event) => onChange({ ...rule, value: event.target.value })}
                />
            ) : (
                <div className='flex items-center rounded-lg bg-surface-secondary/35 px-3 text-xs text-muted'>
                    No value required
                </div>
            )}
            <div className='flex items-center justify-end gap-1'>
                <Button
                    size='sm'
                    variant={rule.negate ? 'secondary' : 'ghost'}
                    onPress={() => onChange({ ...rule, negate: !rule.negate })}
                >
                    NOT
                </Button>
                <Button
                    size='sm'
                    variant={rule.case_sensitive ? 'secondary' : 'ghost'}
                    onPress={() => onChange({ ...rule, case_sensitive: !rule.case_sensitive })}
                >
                    Aa
                </Button>
                <Button
                    isIconOnly
                    aria-label='Remove rule'
                    isDisabled={removeDisabled}
                    size='sm'
                    variant='ghost'
                    onPress={onRemove}
                >
                    <Trash2 className='h-4 w-4 text-danger' />
                </Button>
            </div>
        </div>
    );
}

function ConditionGroups({
    groups,
    groupOperator,
    onChange,
}: {
    groups: RequestConditionGroup[];
    groupOperator: string;
    onChange: (groups: RequestConditionGroup[], groupOperator: string) => void;
}) {
    return (
        <div className='space-y-3'>
            <div className='flex flex-wrap items-center justify-between gap-3'>
                <p className='text-xs leading-5 text-muted'>
                    Leave conditions empty to protect every request.
                </p>
                {groups.length > 1 && (
                    <select
                        aria-label='Condition group operator'
                        className='rounded-md border border-border bg-surface px-2 py-1 text-xs'
                        value={groupOperator}
                        onChange={(event) => onChange(groups, event.target.value)}
                    >
                        <option value='AND'>All groups</option>
                        <option value='OR'>Any group</option>
                    </select>
                )}
            </div>
            {groups.map((group, groupIndex) => (
                <div className='overflow-hidden rounded-xl border border-border/70' key={group.id}>
                    <div className='flex flex-wrap items-center justify-between gap-2 bg-surface-secondary/25 px-4 py-2.5'>
                        <select
                            aria-label={`Condition group ${groupIndex + 1} operator`}
                            className='rounded-md border border-border bg-surface px-2 py-1 text-xs'
                            value={group.operator}
                            onChange={(event) =>
                                onChange(
                                    groups.map((item, index) =>
                                        index === groupIndex
                                            ? { ...item, operator: event.target.value }
                                            : item
                                    ),
                                    groupOperator
                                )
                            }
                        >
                            <option value='AND'>All rules</option>
                            <option value='OR'>Any rule</option>
                        </select>
                        <Button
                            isIconOnly
                            aria-label={`Remove condition group ${groupIndex + 1}`}
                            size='sm'
                            variant='ghost'
                            onPress={() =>
                                onChange(
                                    groups.filter((_, index) => index !== groupIndex),
                                    groupOperator
                                )
                            }
                        >
                            <Trash2 className='h-4 w-4 text-danger' />
                        </Button>
                    </div>
                    {group.rules.map((rule, ruleIndex) => (
                        <RuleRow
                            key={rule.id}
                            removeDisabled={group.rules.length === 1}
                            rule={rule}
                            onChange={(nextRule) =>
                                onChange(
                                    groups.map((item, index) =>
                                        index === groupIndex
                                            ? {
                                                  ...item,
                                                  rules: item.rules.map((current, currentIndex) =>
                                                      currentIndex === ruleIndex
                                                          ? nextRule
                                                          : current
                                                  ),
                                              }
                                            : item
                                    ),
                                    groupOperator
                                )
                            }
                            onRemove={() =>
                                onChange(
                                    groups.map((item, index) =>
                                        index === groupIndex
                                            ? {
                                                  ...item,
                                                  rules: item.rules.filter(
                                                      (_, currentIndex) =>
                                                          currentIndex !== ruleIndex
                                                  ),
                                              }
                                            : item
                                    ),
                                    groupOperator
                                )
                            }
                        />
                    ))}
                    <div className='border-t border-border px-4 py-2.5'>
                        <Button
                            size='sm'
                            variant='ghost'
                            onPress={() =>
                                onChange(
                                    groups.map((item, index) =>
                                        index === groupIndex
                                            ? { ...item, rules: [...item.rules, newRule()] }
                                            : item
                                    ),
                                    groupOperator
                                )
                            }
                        >
                            <Plus className='mr-1.5 h-3.5 w-3.5' /> Add condition
                        </Button>
                    </div>
                </div>
            ))}
            <Button
                variant='secondary'
                onPress={() => onChange([...groups, newConditionGroup()], groupOperator)}
            >
                <Plus className='mr-1.5 h-4 w-4' /> Add condition group
            </Button>
        </div>
    );
}

export function SiteSecuritySettings({
    policy,
    saving,
    onChange,
    onSave,
}: {
    policy: SecurityPolicy;
    saving: boolean;
    onChange: (policy: SecurityPolicy) => void;
    onSave: () => void;
}) {
    const valid = useMemo(
        () =>
            policy.waf.block_status >= 400 &&
            policy.waf.block_status <= 599 &&
            policy.waf.max_body_bytes >= 0 &&
            policy.waf.max_body_bytes <= 1_048_576 &&
            policy.waf.groups.every(
                (group) =>
                    group.id.trim() &&
                    group.rules.length > 0 &&
                    (group.status_code ?? policy.waf.block_status) >= 400 &&
                    (group.status_code ?? policy.waf.block_status) <= 599
            ) &&
            policy.rate_limit.rules.every(
                (rule) =>
                    rule.id.trim() &&
                    rule.requests > 0 &&
                    rule.window_seconds > 0 &&
                    rule.window_seconds <= 3600 &&
                    rule.burst >= 0 &&
                    rule.ban_seconds >= 0
            ),
        [policy]
    );

    const updateWAFGroups = (groups: WAFRuleGroup[]) =>
        onChange({ ...policy, waf: { ...policy.waf, groups } });
    const updateRateRules = (rules: RateLimitRule[]) =>
        onChange({ ...policy, rate_limit: { ...policy.rate_limit, rules } });

    return (
        <ContentCard noPadding>
            <div className='flex flex-col gap-3 border-b border-border px-5 py-4 sm:flex-row sm:items-start sm:justify-between'>
                <div>
                    <div className='flex items-center gap-2'>
                        <ShieldCheck className='h-4 w-4 text-primary' />
                        <h2 className='text-sm font-semibold'>Web application firewall</h2>
                    </div>
                    <p className='mt-1 max-w-2xl text-xs leading-5 text-muted'>
                        Managed attack signatures, custom request expressions and CC protection run
                        on every edge node before cache and origin handling.
                    </p>
                </div>
                <ToggleSwitch
                    isSelected={policy.waf.enabled}
                    label='Enable WAF'
                    onChange={(enabled) => onChange({ ...policy, waf: { ...policy.waf, enabled } })}
                />
            </div>

            <div className='space-y-6 p-5'>
                <div className='grid gap-4 md:grid-cols-3'>
                    <label className='flex flex-col gap-1.5 text-sm font-medium'>
                        <span>Operating mode</span>
                        <select
                            className='w-full rounded-lg border border-border bg-surface-secondary px-3 py-2 text-sm'
                            value={policy.waf.mode}
                            onChange={(event) =>
                                onChange({
                                    ...policy,
                                    waf: { ...policy.waf, mode: event.target.value },
                                })
                            }
                        >
                            <option value='BLOCK'>Block matches</option>
                            <option value='MONITOR'>Monitor only</option>
                        </select>
                    </label>
                    <LabeledInput
                        label='Default block status'
                        max={599}
                        min={400}
                        type='number'
                        value={String(policy.waf.block_status)}
                        onChange={(value) =>
                            onChange({
                                ...policy,
                                waf: { ...policy.waf, block_status: Number(value) },
                            })
                        }
                    />
                    <LabeledInput
                        label='Inspected body bytes'
                        max={1_048_576}
                        min={0}
                        type='number'
                        value={String(policy.waf.max_body_bytes)}
                        onChange={(value) =>
                            onChange({
                                ...policy,
                                waf: { ...policy.waf, max_body_bytes: Number(value) },
                            })
                        }
                    />
                </div>

                <div className='space-y-3'>
                    <div>
                        <h3 className='text-sm font-semibold'>Managed presets</h3>
                        <p className='mt-1 text-xs leading-5 text-muted'>
                            Enable maintained signatures for common web attacks and automated
                            scanners.
                        </p>
                    </div>
                    <div className='grid gap-2 sm:grid-cols-2 xl:grid-cols-3'>
                        {presets.map(([id, label]) => {
                            const selected = policy.waf.presets.includes(id);
                            return (
                                <div
                                    className='flex items-center justify-between gap-3 rounded-xl border border-border/70 px-3.5 py-3 text-sm'
                                    key={id}
                                >
                                    <span>{label}</span>
                                    <ToggleSwitch
                                        isSelected={selected}
                                        label={label}
                                        onChange={(enabled) =>
                                            onChange({
                                                ...policy,
                                                waf: {
                                                    ...policy.waf,
                                                    presets: enabled
                                                        ? [...policy.waf.presets, id]
                                                        : policy.waf.presets.filter(
                                                              (preset) => preset !== id
                                                          ),
                                                },
                                            })
                                        }
                                    />
                                </div>
                            );
                        })}
                    </div>
                </div>

                <div className='space-y-3'>
                    <div className='flex flex-wrap items-start justify-between gap-3'>
                        <div>
                            <h3 className='text-sm font-semibold'>Custom rule groups</h3>
                            <p className='mt-1 text-xs leading-5 text-muted'>
                                Rules inside a group use AND or OR. Groups are evaluated in order.
                            </p>
                        </div>
                        <Button
                            variant='secondary'
                            onPress={() => updateWAFGroups([...policy.waf.groups, newWAFGroup()])}
                        >
                            <Plus className='mr-1.5 h-4 w-4' /> Add WAF group
                        </Button>
                    </div>
                    {policy.waf.groups.length === 0 && (
                        <div className='rounded-xl border border-dashed border-border px-5 py-8 text-center text-sm text-muted'>
                            No custom WAF groups. Managed presets can still protect the site.
                        </div>
                    )}
                    {policy.waf.groups.map((group, groupIndex) => (
                        <div
                            className='overflow-hidden rounded-xl border border-border/70'
                            key={group.id}
                        >
                            <div className='grid gap-3 bg-surface-secondary/25 px-4 py-3 md:grid-cols-[minmax(180px,1fr)_130px_130px_100px_110px_auto]'>
                                <Input
                                    aria-label='WAF group name'
                                    value={group.name}
                                    variant='secondary'
                                    onChange={(event) =>
                                        updateWAFGroups(
                                            policy.waf.groups.map((item, index) =>
                                                index === groupIndex
                                                    ? { ...item, name: event.target.value }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <select
                                    aria-label='WAF group operator'
                                    className='rounded-lg border border-border bg-surface px-3 py-2 text-sm'
                                    value={group.operator}
                                    onChange={(event) =>
                                        updateWAFGroups(
                                            policy.waf.groups.map((item, index) =>
                                                index === groupIndex
                                                    ? { ...item, operator: event.target.value }
                                                    : item
                                            )
                                        )
                                    }
                                >
                                    <option value='AND'>All rules</option>
                                    <option value='OR'>Any rule</option>
                                </select>
                                <select
                                    aria-label='WAF group action'
                                    className='rounded-lg border border-border bg-surface px-3 py-2 text-sm'
                                    value={group.action}
                                    onChange={(event) =>
                                        updateWAFGroups(
                                            policy.waf.groups.map((item, index) =>
                                                index === groupIndex
                                                    ? { ...item, action: event.target.value }
                                                    : item
                                            )
                                        )
                                    }
                                >
                                    <option value='BLOCK'>Block</option>
                                    <option value='MONITOR'>Monitor</option>
                                    <option value='ALLOW'>Allow</option>
                                </select>
                                <Input
                                    aria-label='WAF group response status'
                                    disabled={group.action !== 'BLOCK'}
                                    max={599}
                                    min={400}
                                    type='number'
                                    value={String(group.status_code ?? policy.waf.block_status)}
                                    variant='secondary'
                                    onChange={(event) =>
                                        updateWAFGroups(
                                            policy.waf.groups.map((item, index) =>
                                                index === groupIndex
                                                    ? {
                                                          ...item,
                                                          status_code: Number(event.target.value),
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <ToggleSwitch
                                    isSelected={group.enabled}
                                    label='Enable group'
                                    onChange={(enabled) =>
                                        updateWAFGroups(
                                            policy.waf.groups.map((item, index) =>
                                                index === groupIndex ? { ...item, enabled } : item
                                            )
                                        )
                                    }
                                />
                                <Button
                                    isIconOnly
                                    aria-label='Remove WAF group'
                                    variant='ghost'
                                    onPress={() =>
                                        updateWAFGroups(
                                            policy.waf.groups.filter(
                                                (_, index) => index !== groupIndex
                                            )
                                        )
                                    }
                                >
                                    <Trash2 className='h-4 w-4 text-danger' />
                                </Button>
                            </div>
                            {group.rules.map((rule, ruleIndex) => (
                                <RuleRow
                                    key={rule.id}
                                    removeDisabled={group.rules.length === 1}
                                    rule={rule}
                                    onChange={(nextRule) =>
                                        updateWAFGroups(
                                            policy.waf.groups.map((item, index) =>
                                                index === groupIndex
                                                    ? {
                                                          ...item,
                                                          rules: item.rules.map(
                                                              (current, currentIndex) =>
                                                                  currentIndex === ruleIndex
                                                                      ? nextRule
                                                                      : current
                                                          ),
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                    onRemove={() =>
                                        updateWAFGroups(
                                            policy.waf.groups.map((item, index) =>
                                                index === groupIndex
                                                    ? {
                                                          ...item,
                                                          rules: item.rules.filter(
                                                              (_, currentIndex) =>
                                                                  currentIndex !== ruleIndex
                                                          ),
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                />
                            ))}
                            <div className='border-t border-border px-4 py-2.5'>
                                <Button
                                    size='sm'
                                    variant='ghost'
                                    onPress={() =>
                                        updateWAFGroups(
                                            policy.waf.groups.map((item, index) =>
                                                index === groupIndex
                                                    ? { ...item, rules: [...item.rules, newRule()] }
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

                <div className='space-y-4 border-t border-border pt-6'>
                    <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
                        <div>
                            <div className='flex items-center gap-2'>
                                <Zap className='h-4 w-4 text-primary' />
                                <h3 className='text-sm font-semibold'>
                                    CC and request rate protection
                                </h3>
                            </div>
                            <p className='mt-1 text-xs leading-5 text-muted'>
                                Apply fixed-window limits by client IP, path, Header, Cookie or the
                                entire site.
                            </p>
                        </div>
                        <ToggleSwitch
                            isSelected={policy.rate_limit.enabled}
                            label='Enable CC protection'
                            onChange={(enabled) =>
                                onChange({
                                    ...policy,
                                    rate_limit: { ...policy.rate_limit, enabled },
                                })
                            }
                        />
                    </div>
                    {policy.rate_limit.rules.map((rule, ruleIndex) => (
                        <div
                            className='overflow-hidden rounded-xl border border-border/70'
                            key={rule.id}
                        >
                            <div className='flex flex-wrap items-center justify-between gap-3 bg-surface-secondary/25 px-4 py-3'>
                                <Input
                                    aria-label='Rate-limit rule name'
                                    className='min-w-56 flex-1'
                                    value={rule.name}
                                    variant='secondary'
                                    onChange={(event) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex
                                                    ? { ...item, name: event.target.value }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <ToggleSwitch
                                    isSelected={rule.enabled}
                                    label='Enable rate-limit rule'
                                    onChange={(enabled) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex ? { ...item, enabled } : item
                                            )
                                        )
                                    }
                                />
                                <Button
                                    isIconOnly
                                    aria-label='Remove rate-limit rule'
                                    variant='ghost'
                                    onPress={() =>
                                        updateRateRules(
                                            policy.rate_limit.rules.filter(
                                                (_, index) => index !== ruleIndex
                                            )
                                        )
                                    }
                                >
                                    <Trash2 className='h-4 w-4 text-danger' />
                                </Button>
                            </div>
                            <div className='grid gap-3 p-4 sm:grid-cols-2 xl:grid-cols-4'>
                                <label className='flex flex-col gap-1.5 text-sm font-medium'>
                                    <span>Counter key</span>
                                    <select
                                        className='w-full rounded-lg border border-border bg-surface-secondary px-3 py-2 text-sm'
                                        value={rule.key}
                                        onChange={(event) =>
                                            updateRateRules(
                                                policy.rate_limit.rules.map((item, index) =>
                                                    index === ruleIndex
                                                        ? {
                                                              ...item,
                                                              key: event.target.value,
                                                              key_name: [
                                                                  'HEADER',
                                                                  'COOKIE',
                                                              ].includes(event.target.value)
                                                                  ? item.key_name
                                                                  : undefined,
                                                          }
                                                        : item
                                                )
                                            )
                                        }
                                    >
                                        <option value='CLIENT_IP'>Client IP</option>
                                        <option value='CLIENT_IP_PATH'>Client IP and path</option>
                                        <option value='HEADER'>Header value</option>
                                        <option value='COOKIE'>Cookie value</option>
                                        <option value='GLOBAL'>Entire site</option>
                                    </select>
                                </label>
                                {['HEADER', 'COOKIE'].includes(rule.key) && (
                                    <LabeledInput
                                        label={
                                            rule.key === 'HEADER' ? 'Header name' : 'Cookie name'
                                        }
                                        value={rule.key_name ?? ''}
                                        onChange={(value) =>
                                            updateRateRules(
                                                policy.rate_limit.rules.map((item, index) =>
                                                    index === ruleIndex
                                                        ? { ...item, key_name: value }
                                                        : item
                                                )
                                            )
                                        }
                                    />
                                )}
                                <LabeledInput
                                    label='Requests'
                                    min={1}
                                    type='number'
                                    value={String(rule.requests)}
                                    onChange={(value) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex
                                                    ? {
                                                          ...item,
                                                          requests: Number(value),
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <LabeledInput
                                    label='Window seconds'
                                    max={3600}
                                    min={1}
                                    type='number'
                                    value={String(rule.window_seconds)}
                                    onChange={(value) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex
                                                    ? {
                                                          ...item,
                                                          window_seconds: Number(value),
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <LabeledInput
                                    label='Burst allowance'
                                    min={0}
                                    type='number'
                                    value={String(rule.burst)}
                                    onChange={(value) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex
                                                    ? { ...item, burst: Number(value) }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <LabeledInput
                                    label='Ban seconds'
                                    min={0}
                                    type='number'
                                    value={String(rule.ban_seconds)}
                                    onChange={(value) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex
                                                    ? {
                                                          ...item,
                                                          ban_seconds: Number(value),
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <LabeledInput
                                    label='Response status'
                                    max={599}
                                    min={400}
                                    type='number'
                                    value={String(rule.status_code)}
                                    onChange={(value) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex
                                                    ? {
                                                          ...item,
                                                          status_code: Number(value),
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                />
                            </div>
                            <div className='border-t border-border p-4'>
                                <ConditionGroups
                                    groupOperator={rule.conditions.group_operator}
                                    groups={rule.conditions.groups}
                                    onChange={(groups, groupOperator) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex
                                                    ? {
                                                          ...item,
                                                          conditions: {
                                                              groups,
                                                              group_operator: groupOperator,
                                                          },
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                />
                            </div>
                        </div>
                    ))}
                    <Button
                        variant='secondary'
                        onPress={() =>
                            updateRateRules([...policy.rate_limit.rules, newRateLimitRule()])
                        }
                    >
                        <Plus className='mr-1.5 h-4 w-4' /> Add CC rule
                    </Button>
                </div>
            </div>

            <div className='flex flex-col gap-3 border-t border-border bg-surface px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                <div className={`text-xs ${valid ? 'text-muted' : 'text-danger'}`}>
                    {valid
                        ? 'Saving publishes the security policy to online edge nodes.'
                        : 'Fix invalid status codes, limits or empty rule groups before saving.'}
                </div>
                <Button isDisabled={saving || !valid} onPress={onSave}>
                    <Save className='mr-1.5 h-4 w-4' />{' '}
                    {saving ? 'Saving...' : 'Save security policy'}
                </Button>
            </div>
        </ContentCard>
    );
}
