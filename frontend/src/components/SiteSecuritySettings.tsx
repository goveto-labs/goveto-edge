import type {
    RateLimitRule,
    RequestConditionGroup,
    SecurityPolicy,
    WAFException,
    WAFRequestRule,
    WAFResponse,
    WAFRuleGroup,
} from '@/api';

import { Button, Input } from '@heroui/react';
import { Bot, Plus, Save, ShieldCheck, Trash2, Zap } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import {
    type MultiAddOption,
    SearchableMultiAddField,
} from '@/components/SearchableMultiAddField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { ValueListAddField } from '@/components/ValueListAddField.tsx';
import { countryOptions } from '@/data/countries.ts';

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

const wafActions = [
    ['MONITOR', 'Monitor', 'Record the match and continue without enforcement.'],
    ['SHOW_PAGE', 'Show page', 'Return the built-in block page or custom content.'],
    ['BLOCK', 'Block', 'Stop immediately with an empty error response.'],
    ['CAPTCHA', 'CAPTCHA', 'Run the Scrypt five-second shield and browser integrity checks.'],
    ['REDIRECT', 'Redirect', 'Send the visitor to another URL or path.'],
    ['ALLOW', 'Allow', 'Bypass managed presets and continue the request.'],
    ['TAG', 'TAG', 'Attach a trusted edge tag and continue.'],
] as const;

const httpMethodOptions = [
    ['GET', 'Retrieve a resource'],
    ['HEAD', 'Retrieve response headers'],
    ['POST', 'Submit a resource'],
    ['PUT', 'Replace a resource'],
    ['PATCH', 'Update part of a resource'],
    ['DELETE', 'Delete a resource'],
    ['OPTIONS', 'Inspect supported methods'],
    ['TRACE', 'Diagnostic loopback'],
    ['CONNECT', 'Open a tunnel'],
].map(([id, detail]) => ({ id, name: id, detail }));

function includeSelectedOptions(
    options: MultiAddOption[],
    selected: string[],
    fallbackDetail: string
) {
    const known = new Set(options.map((option) => option.id));
    return [
        ...options,
        ...selected
            .filter((id) => !known.has(id))
            .map((id) => ({ id, name: id, detail: fallbackDetail })),
    ];
}

function newRule(): WAFRequestRule {
    return { id: crypto.randomUUID(), field: 'PATH', operator: 'PREFIX', value: '/' };
}

function newWAFGroup(): WAFRuleGroup {
    return {
        id: crypto.randomUUID(),
        name: 'Custom rule group',
        enabled: true,
        rollout_percentage: 100,
        operator: 'AND',
        action: 'SHOW_PAGE',
        status_code: 403,
        response: { type: 'DEFAULT' },
        rules: [newRule()],
    };
}

function newWAFException(): WAFException {
    return {
        id: crypto.randomUUID(),
        enabled: true,
        rule_ids: [],
        conditions: { group_operator: 'AND', groups: [newConditionGroup()] },
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

function CSVInput({
    label,
    values,
    placeholder,
    onChange,
}: {
    label: string;
    values: string[];
    placeholder?: string;
    onChange: (values: string[]) => void;
}) {
    return (
        <div className='flex flex-col gap-1.5 text-sm font-medium'>
            <span>{label}</span>
            <ValueListInput
                ariaLabel={label}
                placeholder={placeholder}
                values={values}
                onChange={onChange}
            />
        </div>
    );
}

function ValueListInput({
    ariaLabel,
    values,
    placeholder,
    className,
    onChange,
}: {
    ariaLabel: string;
    values: string[];
    placeholder?: string;
    className?: string;
    onChange: (values: string[]) => void;
}) {
    const [draft, setDraft] = useState(values.join(', '));
    const focused = useRef(false);

    useEffect(() => {
        if (!focused.current) setDraft(values.join(', '));
    }, [values]);

    return (
        <Input
            aria-label={ariaLabel}
            className={className}
            placeholder={placeholder}
            value={draft}
            variant='secondary'
            onBlur={() => {
                focused.current = false;
                const normalized = splitValues(draft);
                setDraft(normalized.join(', '));
                onChange(normalized);
            }}
            onChange={(event) => {
                setDraft(event.target.value);
                onChange(splitValues(event.target.value));
            }}
            onFocus={() => {
                focused.current = true;
            }}
        />
    );
}

function isValidResponse(response: WAFResponse) {
    if (response.type === 'DEFAULT') return true;
    if (!response.body || new TextEncoder().encode(response.body).length > 131_072) return false;
    if (response.type !== 'JSON') return true;
    try {
        JSON.parse(response.body);
        return true;
    } catch {
        return false;
    }
}

function LabeledInput({
    label,
    value,
    placeholder,
    type = 'text',
    min,
    max,
    onChange,
}: {
    label: string;
    value: string;
    placeholder?: string;
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
                placeholder={placeholder}
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
        <div className='waf-rule-row grid gap-2 border-t border-border px-4 py-3 first:border-t-0'>
            <SelectField
                ariaLabel='Request field'
                className='min-w-0'
                options={fields.map(([value, label]) => ({ id: value, label }))}
                value={rule.field}
                variant='secondary'
                onChange={(field) => {
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
            />
            <SelectField
                ariaLabel='Match operator'
                className='min-w-0'
                options={operators
                    .filter(([value]) => value !== 'CIDR' || rule.field === 'CLIENT_IP')
                    .map(([value, label]) => ({ id: value, label }))}
                value={rule.operator}
                variant='secondary'
                onChange={(value) =>
                    onChange({
                        ...rule,
                        operator: value,
                        value: value === 'EXISTS' ? undefined : rule.value,
                        values: ['IN', 'CIDR'].includes(value) ? rule.values : undefined,
                    })
                }
            />
            {needsName ? (
                <Input
                    aria-label='Field name'
                    className='min-w-0'
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
                <ValueListInput
                    ariaLabel='Match values'
                    className='min-w-0'
                    placeholder={
                        rule.operator === 'CIDR' ? '192.0.2.0/24, 2001:db8::/32' : 'GET, HEAD'
                    }
                    values={rule.values ?? []}
                    onChange={(values) => onChange({ ...rule, values })}
                />
            ) : needsValue ? (
                <Input
                    aria-label='Match value'
                    className='min-w-0'
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
            <div className='waf-rule-actions flex flex-wrap items-center justify-end gap-1'>
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
                    <SelectField
                        ariaLabel='Condition group operator'
                        className='min-w-32'
                        options={[
                            { id: 'AND', label: 'All groups' },
                            { id: 'OR', label: 'Any group' },
                        ]}
                        value={groupOperator}
                        onChange={(value) => onChange(groups, value)}
                    />
                )}
            </div>
            {groups.map((group, groupIndex) => (
                <div className='overflow-hidden rounded-xl border border-border/70' key={group.id}>
                    <div className='flex flex-wrap items-center justify-between gap-2 bg-surface-secondary/25 px-4 py-2.5'>
                        <SelectField
                            ariaLabel={`Condition group ${groupIndex + 1} operator`}
                            className='min-w-28'
                            options={[
                                { id: 'AND', label: 'All rules' },
                                { id: 'OR', label: 'Any rule' },
                            ]}
                            value={group.operator}
                            onChange={(value) =>
                                onChange(
                                    groups.map((item, index) =>
                                        index === groupIndex ? { ...item, operator: value } : item
                                    ),
                                    groupOperator
                                )
                            }
                        />
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

function ResponseEditor({
    response,
    onChange,
}: {
    response: WAFResponse;
    onChange: (response: WAFResponse) => void;
}) {
    const custom = response.type !== 'DEFAULT';
    return (
        <div className='grid gap-3 lg:grid-cols-[220px_minmax(0,1fr)]'>
            <div>
                <SelectField
                    label='Response content'
                    options={[
                        { id: 'DEFAULT', label: 'Default WAF page' },
                        { id: 'HTML', label: 'Custom HTML' },
                        { id: 'TEXT', label: 'Plain text' },
                        { id: 'JSON', label: 'JSON' },
                    ]}
                    value={response.type}
                    variant='secondary'
                    onChange={(value) =>
                        onChange({
                            type: value as WAFResponse['type'],
                            body: value === 'DEFAULT' ? undefined : response.body,
                        })
                    }
                />
                <span className='mt-1.5 block text-xs font-normal leading-5 text-muted'>
                    {response.type === 'DEFAULT'
                        ? 'Uses the embedded Goveto Edge block page.'
                        : 'Maximum response body size is 128 KiB.'}
                </span>
            </div>
            {custom ? (
                <label className='flex flex-col gap-1.5 text-sm font-medium'>
                    <span>{response.type === 'HTML' ? 'HTML document' : 'Response body'}</span>
                    <textarea
                        className='min-h-36 w-full resize-y rounded-lg border border-border bg-surface-secondary px-3 py-2 font-mono text-xs leading-5 text-foreground outline-none focus:border-primary'
                        placeholder={
                            response.type === 'HTML'
                                ? '<!doctype html>…'
                                : response.type === 'JSON'
                                  ? '{"error":"request blocked"}'
                                  : 'Request blocked by security policy.'
                        }
                        value={response.body ?? ''}
                        onChange={(event) => onChange({ ...response, body: event.target.value })}
                    />
                </label>
            ) : (
                <div className='flex min-h-36 items-center justify-center rounded-lg border border-dashed border-border bg-surface-secondary/20 px-5 text-center text-xs leading-5 text-muted'>
                    The built-in page includes the HTTP status, matched rule reference and a
                    no-index directive.
                </div>
            )}
        </div>
    );
}

function WAFActionEditor({
    group,
    defaultStatus,
    onChange,
}: {
    group: WAFRuleGroup;
    defaultStatus: number;
    onChange: (group: WAFRuleGroup) => void;
}) {
    return (
        <div className='space-y-4'>
            <div className='grid gap-2 sm:grid-cols-2 xl:grid-cols-3'>
                {wafActions.map(([value, label, description]) => {
                    const selected = group.action === value;
                    return (
                        <button
                            aria-pressed={selected}
                            className={`min-h-20 rounded-xl border px-3.5 py-3 text-left transition-colors active:translate-y-px ${
                                selected
                                    ? 'border-primary bg-primary/8 ring-1 ring-primary/20'
                                    : 'border-border/70 bg-surface hover:border-border'
                            }`}
                            key={value}
                            type='button'
                            onClick={() => onChange({ ...group, action: value })}
                        >
                            <span className='block text-sm font-semibold'>{label}</span>
                            <span className='mt-1 block text-xs leading-5 text-muted'>
                                {description}
                            </span>
                        </button>
                    );
                })}
            </div>

            {(group.action === 'SHOW_PAGE' || group.action === 'BLOCK') && (
                <div className='grid gap-4 rounded-xl border border-border/70 bg-surface-secondary/20 p-4'>
                    <LabeledInput
                        label='HTTP response status'
                        max={599}
                        min={400}
                        type='number'
                        value={String(group.status_code ?? defaultStatus)}
                        onChange={(value) => onChange({ ...group, status_code: Number(value) })}
                    />
                    {group.action === 'SHOW_PAGE' && (
                        <ResponseEditor
                            response={group.response ?? { type: 'DEFAULT' }}
                            onChange={(response) => onChange({ ...group, response })}
                        />
                    )}
                </div>
            )}
            {group.action === 'CAPTCHA' && (
                <div className='flex gap-3 rounded-xl border border-primary/25 bg-primary/5 p-4'>
                    <Bot className='mt-0.5 h-5 w-5 shrink-0 text-primary' />
                    <div>
                        <p className='text-sm font-semibold'>Memory-hard browser challenge</p>
                        <p className='mt-1 text-xs leading-5 text-muted'>
                            Runs Scrypt in an isolated Worker, checks high-confidence automation and
                            environment consistency, then grants a 30-minute request-bound
                            clearance.
                        </p>
                    </div>
                </div>
            )}
            {group.action === 'REDIRECT' && (
                <div className='grid gap-3 rounded-xl border border-border/70 bg-surface-secondary/20 p-4 md:grid-cols-[1fr_180px]'>
                    <LabeledInput
                        label='Destination URL or path'
                        value={group.redirect_url ?? ''}
                        onChange={(value) => onChange({ ...group, redirect_url: value })}
                    />
                    <SelectField
                        label='Redirect status'
                        options={[
                            { id: '301', label: '301 Permanent' },
                            { id: '302', label: '302 Temporary' },
                            { id: '303', label: '303 See other' },
                            { id: '307', label: '307 Preserve method' },
                            { id: '308', label: '308 Permanent, preserve method' },
                        ]}
                        value={String(group.redirect_status ?? 302)}
                        variant='secondary'
                        onChange={(value) => onChange({ ...group, redirect_status: Number(value) })}
                    />
                </div>
            )}
            {group.action === 'TAG' && (
                <div className='rounded-xl border border-border/70 bg-surface-secondary/20 p-4'>
                    <LabeledInput
                        label='Edge tag'
                        value={group.tag ?? ''}
                        onChange={(value) => onChange({ ...group, tag: value })}
                    />
                    <p className='mt-2 text-xs leading-5 text-muted'>
                        Added to the upstream request as X-Goveto-WAF-Tags and exposed on the edge
                        response for observability.
                    </p>
                </div>
            )}
            {group.action === 'ALLOW' && (
                <div className='rounded-xl border border-border/70 bg-surface-secondary/20 p-4 text-xs leading-5 text-muted'>
                    Matching requests bypass managed WAF presets and continue to cache and origin
                    handling. Place narrow allow rules before broader blocking groups.
                </div>
            )}
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
            policy.waf.rollout_percentage >= 1 &&
            policy.waf.rollout_percentage <= 100 &&
            isValidResponse(policy.waf.block_response) &&
            policy.waf.groups.every(
                (group) =>
                    group.id.trim() &&
                    group.rollout_percentage >= 1 &&
                    group.rollout_percentage <= 100 &&
                    group.rules.length > 0 &&
                    (!['SHOW_PAGE', 'BLOCK'].includes(group.action) ||
                        ((group.status_code ?? policy.waf.block_status) >= 400 &&
                            (group.status_code ?? policy.waf.block_status) <= 599)) &&
                    (group.action !== 'SHOW_PAGE' ||
                        isValidResponse(group.response ?? { type: 'DEFAULT' })) &&
                    (group.action !== 'REDIRECT' || Boolean(group.redirect_url?.trim())) &&
                    (group.action !== 'TAG' || Boolean(group.tag?.trim()))
            ) &&
            policy.waf.exceptions.every(
                (exception) => exception.id.trim() && exception.rule_ids.length > 0
            ) &&
            policy.access.status_code >= 400 &&
            policy.access.status_code <= 599 &&
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
    const updateWAFGroup = (index: number, group: WAFRuleGroup) =>
        updateWAFGroups(
            policy.waf.groups.map((item, current) => (current === index ? group : item))
        );
    const updateRateRules = (rules: RateLimitRule[]) =>
        onChange({ ...policy, rate_limit: { ...policy.rate_limit, rules } });
    const updateExceptions = (exceptions: WAFException[]) =>
        onChange({ ...policy, waf: { ...policy.waf, exceptions } });

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
                    <SelectField
                        label='Operating mode'
                        options={[
                            { id: 'BLOCK', label: 'Block matches' },
                            { id: 'MONITOR', label: 'Monitor only' },
                        ]}
                        value={policy.waf.mode}
                        variant='secondary'
                        onChange={(value) =>
                            onChange({
                                ...policy,
                                waf: { ...policy.waf, mode: value },
                            })
                        }
                    />
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

                <div>
                    <div className='flex flex-col gap-1.5'>
                        <span className='text-sm font-medium'>Managed rule set</span>
                        <div className='flex min-h-10 items-center gap-2'>
                            <span className='font-mono text-sm'>{policy.waf.rule_set_version}</span>
                            <span className='text-xs text-muted'>Current version</span>
                        </div>
                    </div>
                </div>

                <div className='space-y-3 rounded-xl border border-border/70 p-4'>
                    <div>
                        <h3 className='text-sm font-semibold'>Managed WAF block response</h3>
                        <p className='mt-1 text-xs leading-5 text-muted'>
                            Used when a managed attack preset blocks a request. The default is the
                            embedded Goveto Edge WAF page.
                        </p>
                    </div>
                    <ResponseEditor
                        response={policy.waf.block_response}
                        onChange={(blockResponse) =>
                            onChange({
                                ...policy,
                                waf: { ...policy.waf, block_response: blockResponse },
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
                            className='overflow-hidden rounded-2xl border border-border/70'
                            key={group.id}
                        >
                            <div className='flex flex-col gap-3 border-b border-border bg-surface-secondary/25 px-4 py-3 sm:flex-row sm:items-center'>
                                <Input
                                    aria-label='WAF group name'
                                    className='min-w-0 flex-1'
                                    value={group.name}
                                    variant='secondary'
                                    onChange={(event) =>
                                        updateWAFGroup(groupIndex, {
                                            ...group,
                                            name: event.target.value,
                                        })
                                    }
                                />
                                <ToggleSwitch
                                    isSelected={group.enabled}
                                    label='Enable group'
                                    onChange={(enabled) =>
                                        updateWAFGroup(groupIndex, { ...group, enabled })
                                    }
                                />
                                <Input
                                    aria-label='Group rollout percentage'
                                    className='w-24'
                                    max={100}
                                    min={1}
                                    type='number'
                                    value={String(group.rollout_percentage)}
                                    variant='secondary'
                                    onChange={(event) =>
                                        updateWAFGroup(groupIndex, {
                                            ...group,
                                            rollout_percentage: Number(event.target.value),
                                        })
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
                            <div className='grid 2xl:grid-cols-[minmax(0,1.12fr)_minmax(340px,.88fr)]'>
                                <section className='min-w-0 border-b border-border 2xl:border-r 2xl:border-b-0'>
                                    <div className='flex flex-wrap items-center justify-between gap-3 px-4 py-3'>
                                        <div>
                                            <p className='text-sm font-semibold'>1. Match rules</p>
                                            <p className='mt-0.5 text-xs text-muted'>
                                                Define which requests enter this group.
                                            </p>
                                        </div>
                                        <SelectField
                                            ariaLabel='WAF group operator'
                                            className='min-w-40'
                                            options={[
                                                { id: 'AND', label: 'Match all rules' },
                                                { id: 'OR', label: 'Match any rule' },
                                            ]}
                                            value={group.operator}
                                            variant='secondary'
                                            onChange={(value) =>
                                                updateWAFGroup(groupIndex, {
                                                    ...group,
                                                    operator: value,
                                                })
                                            }
                                        />
                                    </div>
                                    <div className='waf-rule-list border-t border-border'>
                                        {group.rules.map((rule, ruleIndex) => (
                                            <RuleRow
                                                key={rule.id}
                                                removeDisabled={group.rules.length === 1}
                                                rule={rule}
                                                onChange={(nextRule) =>
                                                    updateWAFGroup(groupIndex, {
                                                        ...group,
                                                        rules: group.rules.map((current, index) =>
                                                            index === ruleIndex ? nextRule : current
                                                        ),
                                                    })
                                                }
                                                onRemove={() =>
                                                    updateWAFGroup(groupIndex, {
                                                        ...group,
                                                        rules: group.rules.filter(
                                                            (_, index) => index !== ruleIndex
                                                        ),
                                                    })
                                                }
                                            />
                                        ))}
                                    </div>
                                    <div className='border-t border-border px-4 py-2.5'>
                                        <Button
                                            size='sm'
                                            variant='ghost'
                                            onPress={() =>
                                                updateWAFGroup(groupIndex, {
                                                    ...group,
                                                    rules: [...group.rules, newRule()],
                                                })
                                            }
                                        >
                                            <Plus className='mr-1.5 h-3.5 w-3.5' /> Add rule
                                        </Button>
                                    </div>
                                </section>
                                <section className='min-w-0 bg-surface-secondary/10 p-4'>
                                    <div className='mb-3'>
                                        <p className='text-sm font-semibold'>2. Execute action</p>
                                        <p className='mt-0.5 text-xs text-muted'>
                                            Choose exactly what the edge does after a match.
                                        </p>
                                    </div>
                                    <WAFActionEditor
                                        defaultStatus={policy.waf.block_status}
                                        group={group}
                                        onChange={(nextGroup) =>
                                            updateWAFGroup(groupIndex, nextGroup)
                                        }
                                    />
                                </section>
                            </div>
                        </div>
                    ))}
                </div>

                <div className='space-y-3 border-t border-border pt-6'>
                    <div className='flex flex-wrap items-start justify-between gap-3'>
                        <div>
                            <h3 className='text-sm font-semibold'>Rule exceptions</h3>
                            <p className='mt-1 text-xs leading-5 text-muted'>
                                Skip named managed or custom rules for narrowly matched requests.
                            </p>
                        </div>
                        <Button
                            variant='secondary'
                            onPress={() =>
                                updateExceptions([...policy.waf.exceptions, newWAFException()])
                            }
                        >
                            <Plus className='mr-1.5 h-4 w-4' /> Add exception
                        </Button>
                    </div>
                    {policy.waf.exceptions.length === 0 && (
                        <div className='rounded-xl border border-dashed border-border px-5 py-6 text-center text-sm text-muted'>
                            No rule exceptions are configured.
                        </div>
                    )}
                    {policy.waf.exceptions.map((exception, exceptionIndex) => (
                        <div
                            className='overflow-hidden rounded-xl border border-border/70'
                            key={exception.id}
                        >
                            <div className='flex flex-wrap items-center gap-3 border-b border-border bg-surface-secondary/25 px-4 py-3'>
                                <Input
                                    aria-label='Exception ID'
                                    className='min-w-48 flex-1'
                                    value={exception.id}
                                    variant='secondary'
                                    onChange={(event) =>
                                        updateExceptions(
                                            policy.waf.exceptions.map((item, index) =>
                                                index === exceptionIndex
                                                    ? { ...item, id: event.target.value }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <ToggleSwitch
                                    isSelected={exception.enabled}
                                    label='Enable exception'
                                    onChange={(enabled) =>
                                        updateExceptions(
                                            policy.waf.exceptions.map((item, index) =>
                                                index === exceptionIndex
                                                    ? { ...item, enabled }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <Button
                                    isIconOnly
                                    aria-label='Remove exception'
                                    variant='ghost'
                                    onPress={() =>
                                        updateExceptions(
                                            policy.waf.exceptions.filter(
                                                (_, index) => index !== exceptionIndex
                                            )
                                        )
                                    }
                                >
                                    <Trash2 className='h-4 w-4 text-danger' />
                                </Button>
                            </div>
                            <div className='space-y-4 p-4'>
                                <CSVInput
                                    label='Rule IDs'
                                    placeholder='SQL_INJECTION, custom-rule-id'
                                    values={exception.rule_ids}
                                    onChange={(ruleIds) =>
                                        updateExceptions(
                                            policy.waf.exceptions.map((item, index) =>
                                                index === exceptionIndex
                                                    ? { ...item, rule_ids: ruleIds }
                                                    : item
                                            )
                                        )
                                    }
                                />
                                <ConditionGroups
                                    groupOperator={exception.conditions.group_operator}
                                    groups={exception.conditions.groups}
                                    onChange={(groups, groupOperator) =>
                                        updateExceptions(
                                            policy.waf.exceptions.map((item, index) =>
                                                index === exceptionIndex
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
                </div>

                <div className='space-y-5 border-t border-border pt-6'>
                    <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
                        <div>
                            <h3 className='text-sm font-semibold'>Access control</h3>
                            <p className='mt-1 text-xs leading-5 text-muted'>
                                Enforce network, location, method and hotlink policies before WAF
                                evaluation.
                            </p>
                        </div>
                        <ToggleSwitch
                            isSelected={policy.access.enabled}
                            label='Enable access control'
                            onChange={(enabled) =>
                                onChange({ ...policy, access: { ...policy.access, enabled } })
                            }
                        />
                    </div>

                    <div className='grid gap-4 md:grid-cols-3'>
                        <SelectField
                            label='Operating mode'
                            options={[
                                { id: 'BLOCK', label: 'Block violations' },
                                { id: 'MONITOR', label: 'Monitor only' },
                            ]}
                            value={policy.access.mode}
                            variant='secondary'
                            onChange={(value) =>
                                onChange({
                                    ...policy,
                                    access: { ...policy.access, mode: value },
                                })
                            }
                        />
                        <LabeledInput
                            label='Denied response status'
                            max={599}
                            min={400}
                            type='number'
                            value={String(policy.access.status_code)}
                            onChange={(value) =>
                                onChange({
                                    ...policy,
                                    access: { ...policy.access, status_code: Number(value) },
                                })
                            }
                        />
                        <ValueListAddField
                            addLabel='Add proxy'
                            dialogTitle='Add trusted proxy'
                            emptyLabel='No trusted proxies'
                            label='Trusted proxy CIDRs'
                            placeholder='10.0.0.0/8'
                            values={policy.access.trusted_proxies}
                            onChange={(trustedProxies) =>
                                onChange({
                                    ...policy,
                                    access: {
                                        ...policy.access,
                                        trusted_proxies: trustedProxies,
                                    },
                                })
                            }
                        />
                    </div>

                    <div className='grid gap-4 md:grid-cols-2'>
                        <ValueListAddField
                            addLabel='Add address'
                            dialogTitle='Add allowed IP or CIDR'
                            emptyLabel='No allowed addresses'
                            label='IP/CIDR allowlist'
                            placeholder='192.0.2.10 or 2001:db8::/32'
                            values={policy.access.ip_allowlist}
                            onChange={(ipAllowlist) =>
                                onChange({
                                    ...policy,
                                    access: { ...policy.access, ip_allowlist: ipAllowlist },
                                })
                            }
                        />
                        <ValueListAddField
                            addLabel='Add address'
                            dialogTitle='Add blocked IP or CIDR'
                            emptyLabel='No blocked addresses'
                            label='IP/CIDR blocklist'
                            placeholder='198.51.100.0/24'
                            values={policy.access.ip_blocklist}
                            onChange={(ipBlocklist) =>
                                onChange({
                                    ...policy,
                                    access: { ...policy.access, ip_blocklist: ipBlocklist },
                                })
                            }
                        />
                        <div className='space-y-1.5'>
                            <div className='text-sm font-medium'>Allowed countries</div>
                            <SearchableMultiAddField
                                addLabel='Add countries'
                                dialogSubtitle='Search ISO countries and select those allowed to access this site.'
                                dialogTitle='Select allowed countries'
                                emptyLabel='No country allowlist'
                                itemLabel='country'
                                options={includeSelectedOptions(
                                    countryOptions,
                                    policy.access.allowed_countries,
                                    'Existing country code'
                                )}
                                searchPlaceholder='Search by country name or code'
                                selected={new Set(policy.access.allowed_countries)}
                                onChange={(allowedCountries) =>
                                    onChange({
                                        ...policy,
                                        access: {
                                            ...policy.access,
                                            allowed_countries: Array.from(allowedCountries),
                                        },
                                    })
                                }
                            />
                        </div>
                        <div className='space-y-1.5'>
                            <div className='text-sm font-medium'>Blocked countries</div>
                            <SearchableMultiAddField
                                addLabel='Add countries'
                                dialogSubtitle='Search ISO countries and select those blocked from this site.'
                                dialogTitle='Select blocked countries'
                                emptyLabel='No blocked countries'
                                itemLabel='country'
                                options={includeSelectedOptions(
                                    countryOptions,
                                    policy.access.blocked_countries,
                                    'Existing country code'
                                )}
                                searchPlaceholder='Search by country name or code'
                                selected={new Set(policy.access.blocked_countries)}
                                onChange={(blockedCountries) =>
                                    onChange({
                                        ...policy,
                                        access: {
                                            ...policy.access,
                                            blocked_countries: Array.from(blockedCountries),
                                        },
                                    })
                                }
                            />
                        </div>
                        <ValueListAddField
                            addLabel='Add region'
                            dialogTitle='Add allowed region'
                            emptyLabel='No region allowlist'
                            label='Allowed regions'
                            hint='Use an ISO 3166-2 subdivision code.'
                            placeholder='US-CA'
                            values={policy.access.allowed_regions}
                            normalize={(value) => value.trim().toUpperCase()}
                            validate={(value) =>
                                /^[A-Z]{2}-[A-Z0-9]{1,3}$/.test(value)
                                    ? ''
                                    : 'Enter an ISO 3166-2 code such as US-CA.'
                            }
                            onChange={(allowedRegions) =>
                                onChange({
                                    ...policy,
                                    access: { ...policy.access, allowed_regions: allowedRegions },
                                })
                            }
                        />
                        <ValueListAddField
                            addLabel='Add region'
                            dialogTitle='Add blocked region'
                            emptyLabel='No blocked regions'
                            label='Blocked regions'
                            hint='Use an ISO 3166-2 subdivision code.'
                            placeholder='US-NY'
                            values={policy.access.blocked_regions}
                            normalize={(value) => value.trim().toUpperCase()}
                            validate={(value) =>
                                /^[A-Z]{2}-[A-Z0-9]{1,3}$/.test(value)
                                    ? ''
                                    : 'Enter an ISO 3166-2 code such as US-NY.'
                            }
                            onChange={(blockedRegions) =>
                                onChange({
                                    ...policy,
                                    access: { ...policy.access, blocked_regions: blockedRegions },
                                })
                            }
                        />
                    </div>

                    <div className='grid gap-4 md:grid-cols-2'>
                        <div className='space-y-1.5'>
                            <div className='text-sm font-medium'>Allowed HTTP methods</div>
                            <SearchableMultiAddField
                                addLabel='Add methods'
                                dialogSubtitle='Select the HTTP methods allowed by this access policy.'
                                dialogTitle='Select allowed HTTP methods'
                                emptyLabel='All methods allowed by default'
                                itemLabel='method'
                                options={includeSelectedOptions(
                                    httpMethodOptions,
                                    policy.access.allowed_methods,
                                    'Custom HTTP method'
                                )}
                                searchPlaceholder='Search HTTP methods'
                                selected={new Set(policy.access.allowed_methods)}
                                onChange={(allowedMethods) =>
                                    onChange({
                                        ...policy,
                                        access: {
                                            ...policy.access,
                                            allowed_methods: Array.from(allowedMethods),
                                        },
                                    })
                                }
                            />
                        </div>
                        <div className='space-y-1.5'>
                            <div className='text-sm font-medium'>Blocked HTTP methods</div>
                            <SearchableMultiAddField
                                addLabel='Add methods'
                                dialogSubtitle='Select the HTTP methods blocked by this access policy.'
                                dialogTitle='Select blocked HTTP methods'
                                emptyLabel='No blocked methods'
                                itemLabel='method'
                                options={includeSelectedOptions(
                                    httpMethodOptions,
                                    policy.access.blocked_methods,
                                    'Custom HTTP method'
                                )}
                                searchPlaceholder='Search HTTP methods'
                                selected={new Set(policy.access.blocked_methods)}
                                onChange={(blockedMethods) =>
                                    onChange({
                                        ...policy,
                                        access: {
                                            ...policy.access,
                                            blocked_methods: Array.from(blockedMethods),
                                        },
                                    })
                                }
                            />
                        </div>
                    </div>

                    <div className='grid gap-4 md:grid-cols-[minmax(0,1fr)_auto]'>
                        <ValueListAddField
                            addLabel='Add host'
                            dialogTitle='Add allowed Referer host'
                            emptyLabel='No Referer restrictions'
                            label='Allowed Referer hosts'
                            placeholder='example.com'
                            values={policy.access.allowed_referer_hosts}
                            normalize={(value) => value.trim().toLowerCase().replace(/\.$/, '')}
                            validate={(value) =>
                                /[/:?#@\s]/.test(value)
                                    ? 'Enter a hostname without a protocol, port or path.'
                                    : ''
                            }
                            onChange={(allowedRefererHosts) =>
                                onChange({
                                    ...policy,
                                    access: {
                                        ...policy.access,
                                        allowed_referer_hosts: allowedRefererHosts,
                                    },
                                })
                            }
                        />
                        <div className='flex items-end pb-1'>
                            <ToggleSwitch
                                isSelected={policy.access.allow_empty_referer}
                                label='Allow empty Referer'
                                onChange={(allowEmptyReferer) =>
                                    onChange({
                                        ...policy,
                                        access: {
                                            ...policy.access,
                                            allow_empty_referer: allowEmptyReferer,
                                        },
                                    })
                                }
                            />
                        </div>
                    </div>

                    <div className='grid gap-4 rounded-xl border border-border/70 p-4 md:grid-cols-[auto_minmax(220px,1fr)]'>
                        <div className='flex items-end pb-1'>
                            <ToggleSwitch
                                isSelected={policy.access.temporary_blocks}
                                label='Enforce temporary blocks'
                                onChange={(temporaryBlocks) =>
                                    onChange({
                                        ...policy,
                                        access: {
                                            ...policy.access,
                                            temporary_blocks: temporaryBlocks,
                                        },
                                    })
                                }
                            />
                        </div>
                        <SelectField
                            label='Redis failure policy'
                            options={[
                                { id: 'OPEN', label: 'Fail open' },
                                { id: 'CLOSED', label: 'Fail closed' },
                            ]}
                            value={policy.access.temporary_block_failure}
                            variant='secondary'
                            onChange={(value) =>
                                onChange({
                                    ...policy,
                                    access: {
                                        ...policy.access,
                                        temporary_block_failure: value,
                                    },
                                })
                            }
                        />
                    </div>
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
                    <div className='grid gap-4 rounded-xl border border-border/70 p-4 md:grid-cols-2'>
                        <SelectField
                            label='Counter backend'
                            options={[
                                { id: 'LOCAL', label: 'Local node memory' },
                                { id: 'REDIS', label: 'Distributed Redis' },
                            ]}
                            value={policy.rate_limit.backend}
                            variant='secondary'
                            onChange={(value) =>
                                onChange({
                                    ...policy,
                                    rate_limit: {
                                        ...policy.rate_limit,
                                        backend: value,
                                    },
                                })
                            }
                        />
                        <SelectField
                            label='Redis failure policy'
                            options={[
                                { id: 'OPEN', label: 'Fail open' },
                                { id: 'CLOSED', label: 'Fail closed' },
                                { id: 'LOCAL', label: 'Fall back to local counters' },
                            ]}
                            value={policy.rate_limit.failure_mode}
                            variant='secondary'
                            onChange={(value) =>
                                onChange({
                                    ...policy,
                                    rate_limit: {
                                        ...policy.rate_limit,
                                        failure_mode: value,
                                    },
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
                                <SelectField
                                    label='Counter key'
                                    options={[
                                        { id: 'CLIENT_IP', label: 'Client IP' },
                                        { id: 'CLIENT_IP_PATH', label: 'Client IP and path' },
                                        { id: 'PATH', label: 'Path' },
                                        { id: 'HEADER', label: 'Header value' },
                                        { id: 'COOKIE', label: 'Cookie value' },
                                        { id: 'GLOBAL', label: 'Entire site' },
                                    ]}
                                    value={rule.key}
                                    variant='secondary'
                                    onChange={(value) =>
                                        updateRateRules(
                                            policy.rate_limit.rules.map((item, index) =>
                                                index === ruleIndex
                                                    ? {
                                                          ...item,
                                                          key: value,
                                                          key_name: ['HEADER', 'COOKIE'].includes(
                                                              value
                                                          )
                                                              ? item.key_name
                                                              : undefined,
                                                      }
                                                    : item
                                            )
                                        )
                                    }
                                />
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
