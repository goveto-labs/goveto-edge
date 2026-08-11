import type {
    SecurityPolicy,
    WAFAction,
    WAFCondition,
    WAFConditionGroup,
    WAFConditions,
    WAFResponse,
    WAFRule,
    WAFRuleSet,
} from '@/api';

import {
    closestCenter,
    DndContext,
    type DragEndEvent,
    KeyboardSensor,
    PointerSensor,
    TouchSensor,
    useSensor,
    useSensors,
} from '@dnd-kit/core';
import {
    arrayMove,
    SortableContext,
    sortableKeyboardCoordinates,
    useSortable,
    verticalListSortingStrategy,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { Button, Input } from '@heroui/react';
import {
    ChevronDown,
    ChevronRight,
    GripVertical,
    Plus,
    Save,
    ShieldCheck,
    Trash2,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { SearchableMultiAddField } from '@/components/SearchableMultiAddField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { SettingsActionBar } from '@/components/SettingsActionBar.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { ValueListAddField } from '@/components/ValueListAddField.tsx';
import { countryOptions } from '@/data/countries.ts';

const fields = [
    ['METHOD', 'Method'],
    ['HOST', 'Host'],
    ['PATH', 'Decoded path'],
    ['RAW_QUERY', 'Raw query'],
    ['QUERY', 'Query parameter'],
    ['QUERY_VALUES', 'All decoded query values'],
    ['REQUEST_TARGET', 'Request target variants'],
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

const actions: { id: WAFAction['type']; label: string }[] = [
    { id: 'MONITOR', label: 'Monitor' },
    { id: 'SHOW_PAGE', label: 'Show block page' },
    { id: 'BLOCK', label: 'Block' },
    { id: 'CAPTCHA', label: 'CAPTCHA' },
    { id: 'REDIRECT', label: 'Redirect' },
    { id: 'ALLOW', label: 'Allow' },
    { id: 'TAG', label: 'Tag' },
];

const methodOptions = [
    'GET',
    'HEAD',
    'POST',
    'PUT',
    'PATCH',
    'DELETE',
    'OPTIONS',
    'TRACE',
    'CONNECT',
].map((id) => ({ id, name: id }));

function newAction(status = 403): WAFAction {
    return { type: 'SHOW_PAGE', status_code: status, response: { type: 'DEFAULT' } };
}

function newCondition(): WAFCondition {
    return { id: crypto.randomUUID(), field: 'PATH', operator: 'PREFIX', value: '/' };
}

function newConditionGroup(): WAFConditionGroup {
    return { id: crypto.randomUUID(), operator: 'AND', conditions: [newCondition()] };
}

function newConditions(): WAFConditions {
    return { operator: 'AND', groups: [newConditionGroup()] };
}

function newRule(): WAFRule {
    return {
        id: crypto.randomUUID(),
        name: 'New request rule',
        enabled: true,
        type: 'MATCH',
        conditions: newConditions(),
        action: newAction(),
    };
}

function newRuleSet(): WAFRuleSet {
    return { id: crypto.randomUUID(), name: 'Custom rule set', enabled: true, rules: [newRule()] };
}

function NumericInput({
    label,
    value,
    min,
    max,
    onChange,
}: {
    label: string;
    value: number;
    min: number;
    max: number;
    onChange: (value: number) => void;
}) {
    return (
        <div className='flex flex-col gap-1.5 text-sm font-medium'>
            <span>{label}</span>
            <Input
                aria-label={label}
                max={max}
                min={min}
                type='number'
                value={String(value)}
                variant='secondary'
                onChange={(event) => onChange(Number(event.target.value))}
            />
        </div>
    );
}

function CSVEditor({
    values,
    onChange,
}: {
    values: string[];
    onChange: (values: string[]) => void;
}) {
    const [draft, setDraft] = useState(values.join(', '));
    useEffect(() => setDraft(values.join(', ')), [values]);
    const commit = () =>
        onChange(
            draft
                .split(',')
                .map((value) => value.trim())
                .filter(Boolean)
        );
    return (
        <Input
            aria-label='Match values'
            placeholder='GET, HEAD'
            value={draft}
            variant='secondary'
            onBlur={commit}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
                if (event.key === 'Enter') commit();
            }}
        />
    );
}

function ResponseEditor({
    response,
    onChange,
}: {
    response: WAFResponse;
    onChange: (response: WAFResponse) => void;
}) {
    return (
        <div className='grid gap-3 md:grid-cols-[220px_minmax(0,1fr)]'>
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
                onChange={(type) =>
                    onChange({
                        type: type as WAFResponse['type'],
                        body: type === 'DEFAULT' ? undefined : response.body,
                    })
                }
            />
            {response.type !== 'DEFAULT' && (
                <div className='flex flex-col gap-1.5 text-sm font-medium'>
                    <span>Response body</span>
                    <textarea
                        aria-label='Response body'
                        className='min-h-28 resize-y rounded-lg border border-border bg-surface-secondary px-3 py-2 font-mono text-xs text-foreground outline-none focus:border-primary'
                        value={response.body ?? ''}
                        onChange={(event) => onChange({ ...response, body: event.target.value })}
                    />
                </div>
            )}
        </div>
    );
}

function ActionEditor({ rule, onChange }: { rule: WAFRule; onChange: (rule: WAFRule) => void }) {
    const action = rule.action;
    const update = (next: Partial<WAFAction>) =>
        onChange({ ...rule, action: { ...action, ...next } });
    return (
        <div className='space-y-3'>
            <SelectField
                label='Execute action'
                options={actions}
                value={action.type}
                variant='secondary'
                onChange={(type) => update({ type: type as WAFAction['type'] })}
            />
            {(action.type === 'SHOW_PAGE' || action.type === 'BLOCK') && (
                <div className='space-y-3'>
                    <NumericInput
                        label='HTTP status'
                        max={599}
                        min={400}
                        value={action.status_code ?? (rule.type === 'RATE_LIMIT' ? 429 : 403)}
                        onChange={(status_code) => update({ status_code })}
                    />
                    {action.type === 'SHOW_PAGE' && (
                        <ResponseEditor
                            response={action.response ?? { type: 'DEFAULT' }}
                            onChange={(response) => update({ response })}
                        />
                    )}
                </div>
            )}
            {action.type === 'REDIRECT' && (
                <div className='grid gap-3 md:grid-cols-[minmax(0,1fr)_220px]'>
                    <div className='flex flex-col gap-1.5 text-sm font-medium'>
                        <span>Destination</span>
                        <Input
                            aria-label='Destination'
                            value={action.redirect_url ?? ''}
                            variant='secondary'
                            onChange={(event) => update({ redirect_url: event.target.value })}
                        />
                    </div>
                    <SelectField
                        label='Redirect status'
                        options={[301, 302, 303, 307, 308].map((status) => ({
                            id: String(status),
                            label: String(status),
                        }))}
                        value={String(action.redirect_status ?? 302)}
                        variant='secondary'
                        onChange={(status) => update({ redirect_status: Number(status) })}
                    />
                </div>
            )}
            {action.type === 'TAG' && (
                <div className='flex flex-col gap-1.5 text-sm font-medium'>
                    <span>Edge tag</span>
                    <Input
                        aria-label='Edge tag'
                        value={action.tag ?? ''}
                        variant='secondary'
                        onChange={(event) => update({ tag: event.target.value })}
                    />
                </div>
            )}
        </div>
    );
}

function ConditionEditor({
    condition,
    onChange,
    onRemove,
}: {
    condition: WAFCondition;
    onChange: (condition: WAFCondition) => void;
    onRemove: () => void;
}) {
    const namedField = ['QUERY', 'HEADER', 'COOKIE'].includes(condition.field);
    const listOperator = condition.operator === 'IN' || condition.operator === 'CIDR';
    return (
        <div className='grid gap-3 border-t border-border px-3 py-3 first:border-t-0 sm:grid-cols-2 xl:grid-cols-[minmax(150px,0.8fr)_minmax(150px,0.8fr)_minmax(180px,1fr)_auto]'>
            <SelectField
                label='Request field'
                options={fields.map(([id, label]) => ({ id, label }))}
                value={condition.field}
                variant='secondary'
                onChange={(field) =>
                    onChange({
                        ...condition,
                        field,
                        field_name: ['QUERY', 'HEADER', 'COOKIE'].includes(field)
                            ? condition.field_name
                            : undefined,
                    })
                }
            />
            <SelectField
                label='Operator'
                options={operators
                    .filter(([id]) => id !== 'CIDR' || condition.field === 'CLIENT_IP')
                    .map(([id, label]) => ({ id, label }))}
                value={condition.operator}
                variant='secondary'
                onChange={(operator) => onChange({ ...condition, operator })}
            />
            <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-1'>
                {namedField && (
                    <div className='flex flex-col gap-1.5 text-sm font-medium'>
                        <span>Field name</span>
                        <Input
                            aria-label='Field name'
                            value={condition.field_name ?? ''}
                            variant='secondary'
                            onChange={(event) =>
                                onChange({ ...condition, field_name: event.target.value })
                            }
                        />
                    </div>
                )}
                {condition.operator !== 'EXISTS' && (
                    <div className='flex flex-col gap-1.5 text-sm font-medium'>
                        <span>{listOperator ? 'Values' : 'Match value'}</span>
                        {listOperator ? (
                            <CSVEditor
                                values={condition.values ?? []}
                                onChange={(values) => onChange({ ...condition, values })}
                            />
                        ) : (
                            <Input
                                aria-label='Match value'
                                value={condition.value ?? ''}
                                variant='secondary'
                                onChange={(event) =>
                                    onChange({ ...condition, value: event.target.value })
                                }
                            />
                        )}
                    </div>
                )}
            </div>
            <div className='flex items-end justify-between gap-2 pb-1 xl:justify-end'>
                <div className='flex flex-wrap gap-3'>
                    <ToggleSwitch
                        label='Negate'
                        isSelected={Boolean(condition.negate)}
                        onChange={(negate) => onChange({ ...condition, negate })}
                    />
                    <ToggleSwitch
                        label='Case sensitive'
                        isSelected={Boolean(condition.case_sensitive)}
                        onChange={(case_sensitive) => onChange({ ...condition, case_sensitive })}
                    />
                </div>
                <Button
                    isIconOnly
                    aria-label='Remove condition'
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

function ConditionsEditor({
    conditions,
    optional,
    onChange,
}: {
    conditions: WAFConditions;
    optional: boolean;
    onChange: (conditions: WAFConditions) => void;
}) {
    return (
        <div className='space-y-3'>
            <div className='flex flex-wrap items-end justify-between gap-3'>
                <SelectField
                    label={optional ? 'Optional condition groups' : 'Condition groups'}
                    options={[
                        { id: 'AND', label: 'Match every group (AND)' },
                        { id: 'OR', label: 'Match any group (OR)' },
                    ]}
                    value={conditions.operator}
                    variant='secondary'
                    onChange={(operator) =>
                        onChange({ ...conditions, operator: operator as WAFConditions['operator'] })
                    }
                />
                <Button
                    size='sm'
                    variant='secondary'
                    onPress={() =>
                        onChange({
                            ...conditions,
                            groups: [...conditions.groups, newConditionGroup()],
                        })
                    }
                >
                    <Plus className='h-4 w-4' />
                    Add group
                </Button>
            </div>
            {conditions.groups.map((group, groupIndex) => (
                <div key={group.id} className='overflow-hidden rounded-lg border border-border'>
                    <div className='flex flex-wrap items-end gap-3 bg-surface-secondary/35 px-3 py-2'>
                        <div className='min-w-48 flex-1'>
                            <SelectField
                                label={`Group ${groupIndex + 1}`}
                                options={[
                                    { id: 'AND', label: 'Match every condition (AND)' },
                                    { id: 'OR', label: 'Match any condition (OR)' },
                                ]}
                                value={group.operator}
                                variant='secondary'
                                onChange={(operator) =>
                                    onChange({
                                        ...conditions,
                                        groups: conditions.groups.map((item, index) =>
                                            index === groupIndex
                                                ? {
                                                      ...item,
                                                      operator:
                                                          operator as WAFConditionGroup['operator'],
                                                  }
                                                : item
                                        ),
                                    })
                                }
                            />
                        </div>
                        <Button
                            size='sm'
                            variant='secondary'
                            onPress={() =>
                                onChange({
                                    ...conditions,
                                    groups: conditions.groups.map((item, index) =>
                                        index === groupIndex
                                            ? {
                                                  ...item,
                                                  conditions: [...item.conditions, newCondition()],
                                              }
                                            : item
                                    ),
                                })
                            }
                        >
                            <Plus className='h-4 w-4' />
                            Add condition
                        </Button>
                        <Button
                            isIconOnly
                            aria-label={`Remove group ${groupIndex + 1}`}
                            size='sm'
                            variant='ghost'
                            onPress={() =>
                                onChange({
                                    ...conditions,
                                    groups: conditions.groups.filter(
                                        (_, index) => index !== groupIndex
                                    ),
                                })
                            }
                        >
                            <Trash2 className='h-4 w-4 text-danger' />
                        </Button>
                    </div>
                    {group.conditions.map((condition, conditionIndex) => (
                        <ConditionEditor
                            key={condition.id}
                            condition={condition}
                            onChange={(next) =>
                                onChange({
                                    ...conditions,
                                    groups: conditions.groups.map((item, index) =>
                                        index === groupIndex
                                            ? {
                                                  ...item,
                                                  conditions: item.conditions.map(
                                                      (value, current) =>
                                                          current === conditionIndex ? next : value
                                                  ),
                                              }
                                            : item
                                    ),
                                })
                            }
                            onRemove={() =>
                                onChange({
                                    ...conditions,
                                    groups: conditions.groups.map((item, index) =>
                                        index === groupIndex
                                            ? {
                                                  ...item,
                                                  conditions: item.conditions.filter(
                                                      (_, current) => current !== conditionIndex
                                                  ),
                                              }
                                            : item
                                    ),
                                })
                            }
                        />
                    ))}
                </div>
            ))}
        </div>
    );
}

function RuleEditor({ rule, onChange }: { rule: WAFRule; onChange: (rule: WAFRule) => void }) {
    return (
        <div className='grid gap-5 p-4 lg:grid-cols-[minmax(0,1.65fr)_minmax(280px,0.85fr)]'>
            <div className='space-y-5'>
                <div className='grid gap-3 sm:grid-cols-2'>
                    <div className='flex flex-col gap-1.5 text-sm font-medium'>
                        <span>Rule name</span>
                        <Input
                            aria-label='Rule name'
                            value={rule.name}
                            variant='secondary'
                            onChange={(event) => onChange({ ...rule, name: event.target.value })}
                        />
                    </div>
                    <SelectField
                        label='Rule type'
                        options={[
                            { id: 'MATCH', label: 'Request match' },
                            { id: 'RATE_LIMIT', label: 'Request frequency' },
                        ]}
                        value={rule.type}
                        variant='secondary'
                        onChange={(type) =>
                            onChange(
                                type === 'RATE_LIMIT'
                                    ? {
                                          ...rule,
                                          type,
                                          key: rule.key ?? 'CLIENT_IP_PATH',
                                          requests: rule.requests ?? 60,
                                          window_seconds: rule.window_seconds ?? 60,
                                          burst: rule.burst ?? 20,
                                          backend: rule.backend ?? 'LOCAL',
                                          failure_mode: rule.failure_mode ?? 'LOCAL',
                                      }
                                    : {
                                          ...rule,
                                          type: 'MATCH',
                                          conditions:
                                              rule.conditions.groups.length > 0
                                                  ? rule.conditions
                                                  : newConditions(),
                                      }
                            )
                        }
                    />
                </div>
                {rule.type === 'RATE_LIMIT' && (
                    <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
                        <SelectField
                            label='Counter key'
                            options={[
                                { id: 'CLIENT_IP_PATH', label: 'Client IP + path' },
                                { id: 'CLIENT_IP', label: 'Client IP' },
                                { id: 'PATH', label: 'Path' },
                                { id: 'GLOBAL', label: 'Entire site' },
                                { id: 'HEADER', label: 'Header' },
                                { id: 'COOKIE', label: 'Cookie' },
                            ]}
                            value={rule.key ?? 'CLIENT_IP_PATH'}
                            variant='secondary'
                            onChange={(key) => onChange({ ...rule, key })}
                        />
                        <SelectField
                            label='Counter backend'
                            options={[
                                { id: 'LOCAL', label: 'Local process' },
                                { id: 'REDIS', label: 'Redis shared' },
                            ]}
                            value={rule.backend ?? 'LOCAL'}
                            variant='secondary'
                            onChange={(backend) =>
                                onChange({
                                    ...rule,
                                    backend: backend as WAFRule['backend'],
                                    failure_mode: backend === 'LOCAL' ? 'LOCAL' : rule.failure_mode,
                                })
                            }
                        />
                        {rule.backend === 'REDIS' && (
                            <SelectField
                                label='Redis failure policy'
                                options={[
                                    { id: 'LOCAL', label: 'Use local counter' },
                                    { id: 'OPEN', label: 'Allow requests' },
                                    { id: 'CLOSED', label: 'Reject with 503' },
                                ]}
                                value={rule.failure_mode ?? 'LOCAL'}
                                variant='secondary'
                                onChange={(failure_mode) =>
                                    onChange({
                                        ...rule,
                                        failure_mode: failure_mode as WAFRule['failure_mode'],
                                    })
                                }
                            />
                        )}
                        {(rule.key === 'HEADER' || rule.key === 'COOKIE') && (
                            <div className='flex flex-col gap-1.5 text-sm font-medium'>
                                <span>Key name</span>
                                <Input
                                    aria-label='Key name'
                                    value={rule.key_name ?? ''}
                                    variant='secondary'
                                    onChange={(event) =>
                                        onChange({ ...rule, key_name: event.target.value })
                                    }
                                />
                            </div>
                        )}
                        <NumericInput
                            label='Requests'
                            min={1}
                            max={1_000_000}
                            value={rule.requests ?? 60}
                            onChange={(requests) => onChange({ ...rule, requests })}
                        />
                        <NumericInput
                            label='Window (seconds)'
                            min={1}
                            max={3600}
                            value={rule.window_seconds ?? 60}
                            onChange={(window_seconds) => onChange({ ...rule, window_seconds })}
                        />
                        <NumericInput
                            label='Burst'
                            min={0}
                            max={(rule.requests ?? 60) * 10}
                            value={rule.burst ?? 20}
                            onChange={(burst) => onChange({ ...rule, burst })}
                        />
                    </div>
                )}
                <ConditionsEditor
                    conditions={rule.conditions}
                    optional={rule.type === 'RATE_LIMIT'}
                    onChange={(conditions) => onChange({ ...rule, conditions })}
                />
            </div>
            <div className='border-t border-border pt-4 lg:border-l lg:border-t-0 lg:pl-5 lg:pt-0'>
                <ActionEditor rule={rule} onChange={onChange} />
            </div>
        </div>
    );
}

function SortableRule({
    rule,
    onChange,
    onRemove,
}: {
    rule: WAFRule;
    onChange: (rule: WAFRule) => void;
    onRemove: () => void;
}) {
    const [expanded, setExpanded] = useState(false);
    const sortable = useSortable({ id: rule.id });
    const style = {
        transform: CSS.Transform.toString(sortable.transform),
        transition: sortable.transition,
    };
    return (
        <div
            ref={sortable.setNodeRef}
            style={style}
            className='border-t border-border bg-surface first:border-t-0'
        >
            <div className='flex min-h-12 items-center gap-2 px-3 py-2'>
                <button
                    {...sortable.attributes}
                    {...sortable.listeners}
                    aria-label={`Reorder ${rule.name}`}
                    className='cursor-grab rounded-md p-1.5 text-muted hover:bg-surface-secondary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary'
                    type='button'
                >
                    <GripVertical className='h-4 w-4' />
                </button>
                <button
                    aria-expanded={expanded}
                    className='flex min-w-0 flex-1 items-center gap-2 text-left'
                    type='button'
                    onClick={() => setExpanded((value) => !value)}
                >
                    {expanded ? (
                        <ChevronDown className='h-4 w-4 shrink-0' />
                    ) : (
                        <ChevronRight className='h-4 w-4 shrink-0' />
                    )}
                    <span className='truncate text-sm font-medium'>{rule.name}</span>
                    <span className='shrink-0 text-xs text-muted'>
                        {rule.type === 'RATE_LIMIT'
                            ? 'Frequency'
                            : `${rule.conditions.groups.length} groups · ${rule.conditions.operator}`}
                    </span>
                    <span className='shrink-0 text-xs font-medium text-primary'>
                        {actions.find((action) => action.id === rule.action.type)?.label}
                    </span>
                </button>
                <ToggleSwitch
                    label={`Enable ${rule.name}`}
                    isSelected={rule.enabled}
                    onChange={(enabled) => onChange({ ...rule, enabled })}
                />
                <Button
                    isIconOnly
                    aria-label={`Remove ${rule.name}`}
                    size='sm'
                    variant='ghost'
                    onPress={onRemove}
                >
                    <Trash2 className='h-4 w-4 text-danger' />
                </Button>
            </div>
            {expanded && <RuleEditor rule={rule} onChange={onChange} />}
        </div>
    );
}

function SortableRuleSet({
    ruleSet,
    onChange,
    onRemove,
}: {
    ruleSet: WAFRuleSet;
    onChange: (ruleSet: WAFRuleSet) => void;
    onRemove: () => void;
}) {
    const [expanded, setExpanded] = useState(true);
    const sortable = useSortable({ id: ruleSet.id });
    const sensors = useSensors(
        useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
        useSensor(TouchSensor, { activationConstraint: { delay: 180, tolerance: 5 } }),
        useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
    );
    const style = {
        transform: CSS.Transform.toString(sortable.transform),
        transition: sortable.transition,
    };
    const reorderRules = ({ active, over }: DragEndEvent) => {
        if (!over || active.id === over.id) return;
        const from = ruleSet.rules.findIndex((rule) => rule.id === active.id);
        const to = ruleSet.rules.findIndex((rule) => rule.id === over.id);
        if (from >= 0 && to >= 0)
            onChange({ ...ruleSet, rules: arrayMove(ruleSet.rules, from, to) });
    };
    return (
        <section
            ref={sortable.setNodeRef}
            style={style}
            className='overflow-hidden rounded-lg border border-border bg-surface'
        >
            <div className='flex flex-wrap items-center gap-2 bg-surface-secondary/35 px-3 py-3'>
                <button
                    {...sortable.attributes}
                    {...sortable.listeners}
                    aria-label={`Reorder ${ruleSet.name}`}
                    className='cursor-grab rounded-md p-1.5 text-muted hover:bg-surface hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary'
                    type='button'
                >
                    <GripVertical className='h-4 w-4' />
                </button>
                <button
                    aria-expanded={expanded}
                    className='flex min-w-0 flex-1 items-center gap-2 text-left'
                    type='button'
                    onClick={() => setExpanded((value) => !value)}
                >
                    {expanded ? (
                        <ChevronDown className='h-4 w-4' />
                    ) : (
                        <ChevronRight className='h-4 w-4' />
                    )}
                    <span className='truncate text-sm font-semibold'>{ruleSet.name}</span>
                    <span className='text-xs text-muted'>{ruleSet.rules.length} rules</span>
                </button>
                <ToggleSwitch
                    label={`Enable ${ruleSet.name}`}
                    isSelected={ruleSet.enabled}
                    onChange={(enabled) => onChange({ ...ruleSet, enabled })}
                />
                <Button
                    size='sm'
                    variant='secondary'
                    onPress={() => onChange({ ...ruleSet, rules: [...ruleSet.rules, newRule()] })}
                >
                    <Plus className='h-4 w-4' />
                    Add rule
                </Button>
                <Button
                    isIconOnly
                    aria-label={`Remove ${ruleSet.name}`}
                    size='sm'
                    variant='ghost'
                    onPress={onRemove}
                >
                    <Trash2 className='h-4 w-4 text-danger' />
                </Button>
            </div>
            {expanded && (
                <DndContext
                    collisionDetection={closestCenter}
                    sensors={sensors}
                    onDragEnd={reorderRules}
                >
                    <SortableContext
                        items={ruleSet.rules.map((rule) => rule.id)}
                        strategy={verticalListSortingStrategy}
                    >
                        {ruleSet.rules.length === 0 ? (
                            <div className='px-4 py-8 text-center text-sm text-muted'>
                                No rules in this rule set.
                            </div>
                        ) : (
                            ruleSet.rules.map((rule, index) => (
                                <SortableRule
                                    key={rule.id}
                                    rule={rule}
                                    onChange={(next) =>
                                        onChange({
                                            ...ruleSet,
                                            rules: ruleSet.rules.map((item, current) =>
                                                current === index ? next : item
                                            ),
                                        })
                                    }
                                    onRemove={() =>
                                        onChange({
                                            ...ruleSet,
                                            rules: ruleSet.rules.filter(
                                                (_, current) => current !== index
                                            ),
                                        })
                                    }
                                />
                            ))
                        )}
                    </SortableContext>
                </DndContext>
            )}
        </section>
    );
}

function AccessEditor({
    policy,
    onChange,
}: {
    policy: SecurityPolicy;
    onChange: (policy: SecurityPolicy) => void;
}) {
    const update = (next: Partial<SecurityPolicy['access']>) =>
        onChange({ ...policy, access: { ...policy.access, ...next } });
    const list = (label: string, key: keyof SecurityPolicy['access'], placeholder: string) => (
        <ValueListAddField
            label={label}
            values={policy.access[key] as string[]}
            addLabel='Add'
            dialogTitle={`Add ${label}`}
            emptyLabel='None configured'
            placeholder={placeholder}
            onChange={(values) => update({ [key]: values })}
        />
    );
    return (
        <ContentCard noPadding>
            <div className='flex flex-col gap-3 border-b border-border px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                <h2 className='text-sm font-semibold'>Access control</h2>
                <ToggleSwitch
                    label='Enable access control'
                    isSelected={policy.access.enabled}
                    onChange={(enabled) => update({ enabled })}
                />
            </div>
            <div className='space-y-5 p-5'>
                <div className='grid gap-4 md:grid-cols-3'>
                    <SelectField
                        label='Operating mode'
                        options={[
                            { id: 'BLOCK', label: 'Block violations' },
                            { id: 'MONITOR', label: 'Monitor only' },
                        ]}
                        value={policy.access.mode}
                        variant='secondary'
                        onChange={(mode) => update({ mode })}
                    />
                    <NumericInput
                        label='Denied status'
                        min={400}
                        max={599}
                        value={policy.access.status_code}
                        onChange={(status_code) => update({ status_code })}
                    />
                    {list('Trusted proxy CIDRs', 'trusted_proxies', '10.0.0.0/8')}
                </div>
                <div className='grid gap-4 md:grid-cols-2'>
                    {list('IP/CIDR allowlist', 'ip_allowlist', '192.0.2.0/24')}
                    {list('IP/CIDR blocklist', 'ip_blocklist', '198.51.100.0/24')}
                </div>
                <div className='grid gap-4 md:grid-cols-2'>
                    <div>
                        <div className='mb-1.5 text-sm font-medium'>Allowed countries</div>
                        <SearchableMultiAddField
                            options={countryOptions}
                            selected={new Set(policy.access.allowed_countries)}
                            addLabel='Add countries'
                            dialogTitle='Allowed countries'
                            itemLabel='country'
                            searchPlaceholder='Search countries'
                            emptyLabel='All countries'
                            onChange={(value) => update({ allowed_countries: Array.from(value) })}
                        />
                    </div>
                    <div>
                        <div className='mb-1.5 text-sm font-medium'>Blocked countries</div>
                        <SearchableMultiAddField
                            options={countryOptions}
                            selected={new Set(policy.access.blocked_countries)}
                            addLabel='Add countries'
                            dialogTitle='Blocked countries'
                            itemLabel='country'
                            searchPlaceholder='Search countries'
                            emptyLabel='No blocked countries'
                            onChange={(value) => update({ blocked_countries: Array.from(value) })}
                        />
                    </div>
                </div>
                <div className='grid gap-4 md:grid-cols-2'>
                    {list('Allowed regions', 'allowed_regions', 'US-NY')}
                    {list('Blocked regions', 'blocked_regions', 'US-NY')}
                </div>
                <div className='grid gap-4 md:grid-cols-2'>
                    <div>
                        <div className='mb-1.5 text-sm font-medium'>Allowed methods</div>
                        <SearchableMultiAddField
                            options={methodOptions}
                            selected={new Set(policy.access.allowed_methods)}
                            addLabel='Add methods'
                            dialogTitle='Allowed methods'
                            itemLabel='method'
                            searchPlaceholder='Search methods'
                            emptyLabel='All methods'
                            onChange={(value) => update({ allowed_methods: Array.from(value) })}
                        />
                    </div>
                    <div>
                        <div className='mb-1.5 text-sm font-medium'>Blocked methods</div>
                        <SearchableMultiAddField
                            options={methodOptions}
                            selected={new Set(policy.access.blocked_methods)}
                            addLabel='Add methods'
                            dialogTitle='Blocked methods'
                            itemLabel='method'
                            searchPlaceholder='Search methods'
                            emptyLabel='No blocked methods'
                            onChange={(value) => update({ blocked_methods: Array.from(value) })}
                        />
                    </div>
                </div>
                <div className='grid gap-4 md:grid-cols-[minmax(0,1fr)_auto]'>
                    {list('Allowed Referer hosts', 'allowed_referer_hosts', 'example.com')}
                    <div className='flex items-end pb-1'>
                        <ToggleSwitch
                            label='Allow empty Referer'
                            isSelected={policy.access.allow_empty_referer}
                            onChange={(allow_empty_referer) => update({ allow_empty_referer })}
                        />
                    </div>
                </div>
                <div className='grid gap-4 border-t border-border pt-5 md:grid-cols-[auto_minmax(220px,1fr)]'>
                    <ToggleSwitch
                        label='Enforce temporary blocks'
                        isSelected={policy.access.temporary_blocks}
                        onChange={(temporary_blocks) => update({ temporary_blocks })}
                    />
                    <SelectField
                        label='Redis failure policy'
                        options={[
                            { id: 'OPEN', label: 'Fail open' },
                            { id: 'CLOSED', label: 'Fail closed' },
                        ]}
                        value={policy.access.temporary_block_failure}
                        variant='secondary'
                        onChange={(temporary_block_failure) => update({ temporary_block_failure })}
                    />
                </div>
            </div>
        </ContentCard>
    );
}

function conditionValid(condition: WAFCondition) {
    if (!condition.field || !condition.operator) return false;
    if (['QUERY', 'HEADER', 'COOKIE'].includes(condition.field) && !condition.field_name?.trim())
        return false;
    if (condition.operator === 'EXISTS') return true;
    return condition.operator === 'IN' || condition.operator === 'CIDR'
        ? Boolean(condition.values?.length)
        : Boolean(condition.value?.trim());
}

function conditionsValid(conditions: WAFConditions, required: boolean) {
    if (required && conditions.groups.length === 0) return false;
    return conditions.groups.every(
        (group) => group.conditions.length > 0 && group.conditions.every(conditionValid)
    );
}

function ruleValid(rule: WAFRule) {
    if (!rule.id.trim() || !rule.name.trim() || !rule.action.type) return false;
    if (!conditionsValid(rule.conditions, rule.type === 'MATCH')) return false;
    if (rule.type === 'RATE_LIMIT') {
        return Boolean(
            rule.key &&
                rule.backend &&
                (rule.requests ?? 0) >= 1 &&
                (rule.window_seconds ?? 0) >= 1 &&
                (rule.burst ?? -1) >= 0
        );
    }
    return true;
}

export function SiteSecuritySettings({
    policy,
    isDirty,
    saving,
    onChange,
    onDiscard,
    onSave,
}: {
    policy: SecurityPolicy;
    isDirty: boolean;
    saving: boolean;
    onChange: (policy: SecurityPolicy) => void;
    onDiscard: () => void;
    onSave: () => void;
}) {
    const sensors = useSensors(
        useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
        useSensor(TouchSensor, { activationConstraint: { delay: 180, tolerance: 5 } }),
        useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
    );
    const valid = useMemo(
        () =>
            policy.waf.rule_sets.every(
                (set) => set.id.trim() && set.name.trim() && set.rules.every(ruleValid)
            ) &&
            policy.access.status_code >= 400 &&
            policy.access.status_code <= 599,
        [policy]
    );
    const reorderSets = ({ active, over }: DragEndEvent) => {
        if (!over || active.id === over.id) return;
        const from = policy.waf.rule_sets.findIndex((set) => set.id === active.id);
        const to = policy.waf.rule_sets.findIndex((set) => set.id === over.id);
        if (from >= 0 && to >= 0)
            onChange({
                ...policy,
                waf: { ...policy.waf, rule_sets: arrayMove(policy.waf.rule_sets, from, to) },
            });
    };
    const updateSets = (rule_sets: WAFRuleSet[]) =>
        onChange({ ...policy, waf: { ...policy.waf, rule_sets } });
    return (
        <div className='space-y-8'>
            <ContentCard noPadding>
                <div className='flex flex-col gap-3 border-b border-border px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                    <div className='flex items-center gap-2'>
                        <ShieldCheck className='h-4 w-4 text-primary' />
                        <h2 className='text-sm font-semibold'>Web application firewall</h2>
                    </div>
                    <ToggleSwitch
                        label='Enable WAF'
                        isSelected={policy.waf.enabled}
                        onChange={(enabled) =>
                            onChange({ ...policy, waf: { ...policy.waf, enabled } })
                        }
                    />
                </div>
                <div className='space-y-4 p-5'>
                    <div className='flex justify-end'>
                        <Button
                            variant='secondary'
                            onPress={() => updateSets([...policy.waf.rule_sets, newRuleSet()])}
                        >
                            <Plus className='h-4 w-4' />
                            Add rule set
                        </Button>
                    </div>
                    <DndContext
                        collisionDetection={closestCenter}
                        sensors={sensors}
                        onDragEnd={reorderSets}
                    >
                        <SortableContext
                            items={policy.waf.rule_sets.map((set) => set.id)}
                            strategy={verticalListSortingStrategy}
                        >
                            <div className='space-y-3'>
                                {policy.waf.rule_sets.length === 0 ? (
                                    <div className='rounded-lg border border-dashed border-border px-5 py-10 text-center text-sm text-muted'>
                                        No WAF rule sets.
                                    </div>
                                ) : (
                                    policy.waf.rule_sets.map((set, index) => (
                                        <SortableRuleSet
                                            key={set.id}
                                            ruleSet={set}
                                            onChange={(next) =>
                                                updateSets(
                                                    policy.waf.rule_sets.map((item, current) =>
                                                        current === index ? next : item
                                                    )
                                                )
                                            }
                                            onRemove={() =>
                                                updateSets(
                                                    policy.waf.rule_sets.filter(
                                                        (_, current) => current !== index
                                                    )
                                                )
                                            }
                                        />
                                    ))
                                )}
                            </div>
                        </SortableContext>
                    </DndContext>
                </div>
            </ContentCard>
            <AccessEditor policy={policy} onChange={onChange} />
            <SettingsActionBar
                error={!valid ? 'Complete every rule and action before saving.' : undefined}
                isDirty={isDirty}
                isDiscardDisabled={saving}
                onDiscard={onDiscard}
            >
                <Button isDisabled={!valid || saving} onPress={onSave}>
                    <Save className='h-4 w-4' />
                    {saving ? 'Saving...' : 'Save security'}
                </Button>
            </SettingsActionBar>
        </div>
    );
}
