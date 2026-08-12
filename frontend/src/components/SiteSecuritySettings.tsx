import type {
    SecurityPolicy,
    WAFAction,
    WAFCondition,
    WAFConditionGroup,
    WAFConditions,
    WAFDSLRenderResponse,
    WAFDSLRequest,
    WAFDSLValidationResponse,
    WAFResponse,
    WAFRule,
    WAFRuleSet,
} from '@/api';
import type { WAFDSLEditRequest } from '@/components/WAFDSLEditorDialog.tsx';

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
import { Button, Input, TextArea, Tooltip } from '@heroui/react';
import {
    ArrowLeft,
    Braces,
    ChevronDown,
    ChevronRight,
    GripVertical,
    Pencil,
    Plus,
    Save,
    ShieldCheck,
    Trash2,
} from 'lucide-react';
import { lazy, Suspense, useEffect, useMemo, useState } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { SettingsActionBar } from '@/components/SettingsActionBar.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

const WAFDSLEditorDialog = lazy(() =>
    import('@/components/WAFDSLEditorDialog.tsx').then((module) => ({
        default: module.WAFDSLEditorDialog,
    }))
);

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
    ['COUNTRY', 'Country'],
    ['REGION', 'Region'],
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
    return { id: crypto.randomUUID(), name: 'Custom rule set', enabled: true, rules: [] };
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
}: {
    condition: WAFCondition;
    onChange: (condition: WAFCondition) => void;
}) {
    const namedField = ['QUERY', 'HEADER', 'COOKIE'].includes(condition.field);
    const listOperator = condition.operator === 'IN' || condition.operator === 'CIDR';
    return (
        <div className='grid min-w-0 gap-4 sm:grid-cols-2'>
            <SelectField
                className='min-w-0'
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
                className='min-w-0'
                label='Operator'
                options={operators
                    .filter(([id]) => id !== 'CIDR' || condition.field === 'CLIENT_IP')
                    .map(([id, label]) => ({ id, label }))}
                value={condition.operator}
                variant='secondary'
                onChange={(operator) => onChange({ ...condition, operator })}
            />
            {namedField && (
                <div className='flex min-w-0 flex-col gap-1.5 text-sm font-medium'>
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
                <div className='flex min-w-0 flex-col gap-1.5 text-sm font-medium sm:col-span-2'>
                    <span>{listOperator ? 'Values' : 'Match value'}</span>
                    {listOperator ? (
                        <CSVEditor
                            values={condition.values ?? []}
                            onChange={(values) => onChange({ ...condition, values })}
                        />
                    ) : (
                        <TextArea
                            aria-label='Match value'
                            className='w-full font-mono text-xs'
                            rows={6}
                            spellCheck={false}
                            value={condition.value ?? ''}
                            variant='secondary'
                            onChange={(event) =>
                                onChange({ ...condition, value: event.target.value })
                            }
                        />
                    )}
                </div>
            )}
            <div className='grid gap-3 sm:col-span-2 sm:grid-cols-2'>
                <div className='flex min-w-0 items-center justify-between gap-4 rounded-lg border border-border px-3 py-3'>
                    <div className='min-w-0'>
                        <div className='text-sm font-medium'>Negate result</div>
                        <div className='text-xs text-muted'>
                            Match when this condition is false.
                        </div>
                    </div>
                    <ToggleSwitch
                        label='Negate condition result'
                        isSelected={Boolean(condition.negate)}
                        onChange={(negate) => onChange({ ...condition, negate })}
                    />
                </div>
                <div className='flex min-w-0 items-center justify-between gap-4 rounded-lg border border-border px-3 py-3'>
                    <div className='min-w-0'>
                        <div className='text-sm font-medium'>Case sensitive</div>
                        <div className='text-xs text-muted'>
                            Match uppercase and lowercase exactly.
                        </div>
                    </div>
                    <ToggleSwitch
                        label='Use case-sensitive matching'
                        isSelected={Boolean(condition.case_sensitive)}
                        onChange={(case_sensitive) => onChange({ ...condition, case_sensitive })}
                    />
                </div>
            </div>
        </div>
    );
}

function conditionSummary(condition: WAFCondition) {
    const field = fields.find(([id]) => id === condition.field)?.[1] ?? condition.field;
    const operator = operators.find(([id]) => id === condition.operator)?.[1] ?? condition.operator;
    const target = condition.field_name ? `${field} "${condition.field_name}"` : field;
    const value =
        condition.operator === 'EXISTS'
            ? ''
            : condition.operator === 'IN' || condition.operator === 'CIDR'
              ? (condition.values ?? []).join(', ')
              : (condition.value ?? '');
    return `${condition.negate ? 'NOT ' : ''}${target} ${operator}${value ? ` ${value}` : ''}`;
}

function groupSummary(group: WAFConditionGroup) {
    if (group.conditions.length === 0) return 'No conditions';
    return group.conditions.map(conditionSummary).join('  /  ');
}

function SortablePreviewRow({
    id,
    title,
    meta,
    detail,
    badges = [],
    onEdit,
    onEditDSL,
    onRemove,
}: {
    id: string;
    title: string;
    meta: string;
    detail: string;
    badges?: string[];
    onEdit: () => void;
    onEditDSL?: () => void;
    onRemove: () => void;
}) {
    const sortable = useSortable({ id });
    return (
        <div
            ref={sortable.setNodeRef}
            style={{
                transform: CSS.Transform.toString(sortable.transform),
                transition: sortable.transition,
            }}
            className='flex min-w-0 items-center gap-2 border-t border-border px-3 py-3 first:border-t-0'
        >
            <button
                {...sortable.attributes}
                {...sortable.listeners}
                aria-label={`Reorder ${title}`}
                className='shrink-0 cursor-grab rounded-md p-1.5 text-muted hover:bg-surface-secondary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary'
                type='button'
            >
                <GripVertical className='h-4 w-4' />
            </button>
            <button className='min-w-0 flex-1 text-left' type='button' onClick={onEdit}>
                <span className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1'>
                    <span className='truncate text-sm font-medium'>{title}</span>
                    <span className='shrink-0 text-xs text-muted'>{meta}</span>
                    {badges.map((badge) => (
                        <span
                            key={badge}
                            className='shrink-0 rounded bg-surface-secondary px-1.5 py-0.5 text-[11px] font-medium text-muted'
                        >
                            {badge}
                        </span>
                    ))}
                </span>
                <span className='mt-1 block truncate text-xs text-muted'>{detail}</span>
            </button>
            <Button
                isIconOnly
                aria-label={`Edit ${title}`}
                size='sm'
                variant='ghost'
                onPress={onEdit}
            >
                <Pencil className='h-4 w-4' />
            </Button>
            {onEditDSL && (
                <Tooltip>
                    <Tooltip.Trigger>
                        <Button
                            isIconOnly
                            aria-label={`Edit ${title} as DSL`}
                            size='sm'
                            variant='ghost'
                            onPress={onEditDSL}
                        >
                            <Braces className='h-4 w-4' />
                        </Button>
                    </Tooltip.Trigger>
                    <Tooltip.Content>Edit as DSL</Tooltip.Content>
                </Tooltip>
            )}
            <Button
                isIconOnly
                aria-label={`Remove ${title}`}
                size='sm'
                variant='ghost'
                onPress={onRemove}
            >
                <Trash2 className='h-4 w-4 text-danger' />
            </Button>
        </div>
    );
}

function ConditionsEditor({
    conditions,
    optional,
    onChange,
    onAdd,
    onEdit,
    onEditDSL,
}: {
    conditions: WAFConditions;
    optional: boolean;
    onChange: (conditions: WAFConditions) => void;
    onAdd: () => void;
    onEdit: (index: number) => void;
    onEditDSL: (index: number) => void;
}) {
    const sensors = useSensors(
        useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
        useSensor(TouchSensor, { activationConstraint: { delay: 180, tolerance: 5 } }),
        useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
    );
    const reorder = ({ active, over }: DragEndEvent) => {
        if (!over || active.id === over.id) return;
        const from = conditions.groups.findIndex((group) => group.id === active.id);
        const to = conditions.groups.findIndex((group) => group.id === over.id);
        if (from >= 0 && to >= 0)
            onChange({ ...conditions, groups: arrayMove(conditions.groups, from, to) });
    };
    return (
        <div className='space-y-3'>
            <div className='flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
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
                <Button size='sm' variant='secondary' onPress={onAdd}>
                    <Plus className='h-4 w-4' />
                    Add group
                </Button>
            </div>
            <div className='overflow-hidden rounded-lg border border-border'>
                {conditions.groups.length === 0 ? (
                    <div className='px-4 py-8 text-center text-sm text-muted'>
                        No condition groups. Add one to define when this rule matches.
                    </div>
                ) : (
                    <DndContext
                        collisionDetection={closestCenter}
                        sensors={sensors}
                        onDragEnd={reorder}
                    >
                        <SortableContext
                            items={conditions.groups.map((group) => group.id)}
                            strategy={verticalListSortingStrategy}
                        >
                            {conditions.groups.map((group, index) => (
                                <SortablePreviewRow
                                    key={group.id}
                                    detail={groupSummary(group)}
                                    id={group.id}
                                    meta={`${group.conditions.length} condition${group.conditions.length === 1 ? '' : 's'}`}
                                    title={`Group ${index + 1} · ${group.operator}`}
                                    onEdit={() => onEdit(index)}
                                    onEditDSL={() => onEditDSL(index)}
                                    onRemove={() =>
                                        onChange({
                                            ...conditions,
                                            groups: conditions.groups.filter(
                                                (_, current) => current !== index
                                            ),
                                        })
                                    }
                                />
                            ))}
                        </SortableContext>
                    </DndContext>
                )}
            </div>
        </div>
    );
}

function RuleEditor({
    rule,
    onChange,
    onAddGroup,
    onEditGroup,
    onEditGroupDSL,
}: {
    rule: WAFRule;
    onChange: (rule: WAFRule) => void;
    onAddGroup: () => void;
    onEditGroup: (index: number) => void;
    onEditGroupDSL: (index: number) => void;
}) {
    return (
        <div className='space-y-6'>
            <section className='space-y-4'>
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
                    onAdd={onAddGroup}
                    onEdit={onEditGroup}
                    onEditDSL={onEditGroupDSL}
                />
            </section>
            <section className='space-y-3 border-t border-border pt-5'>
                <div>
                    <h3 className='text-sm font-semibold'>Action</h3>
                    <p className='mt-1 text-xs text-muted'>
                        Run this action when the rule matches.
                    </p>
                </div>
                <ActionEditor rule={rule} onChange={onChange} />
            </section>
        </div>
    );
}

function SortableRule({
    rule,
    onChange,
    onEdit,
    onEditDSL,
    onRemove,
}: {
    rule: WAFRule;
    onChange: (rule: WAFRule) => void;
    onEdit: () => void;
    onEditDSL: () => void;
    onRemove: () => void;
}) {
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
            <div className='flex min-h-14 items-center gap-2 px-3 py-2.5'>
                <button
                    {...sortable.attributes}
                    {...sortable.listeners}
                    aria-label={`Reorder ${rule.name}`}
                    className='cursor-grab rounded-md p-1.5 text-muted hover:bg-surface-secondary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary'
                    type='button'
                >
                    <GripVertical className='h-4 w-4' />
                </button>
                <button className='min-w-0 flex-1 text-left' type='button' onClick={onEdit}>
                    <span className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1'>
                        <span className='truncate text-sm font-medium'>{rule.name}</span>
                        <span className='shrink-0 text-xs text-muted'>
                            {rule.type === 'RATE_LIMIT' ? 'Frequency' : 'Request match'}
                        </span>
                        <span className='shrink-0 text-xs font-medium text-primary'>
                            {actions.find((action) => action.id === rule.action.type)?.label}
                        </span>
                    </span>
                    <span className='mt-1 block truncate text-xs text-muted'>
                        {rule.conditions.groups.length === 0
                            ? rule.type === 'RATE_LIMIT'
                                ? 'Applies to every request'
                                : 'No condition groups'
                            : `${rule.conditions.groups.length} group${rule.conditions.groups.length === 1 ? '' : 's'} · ${rule.conditions.operator} · ${rule.conditions.groups.map(groupSummary).join(' / ')}`}
                    </span>
                </button>
                <ToggleSwitch
                    label={`Enable ${rule.name}`}
                    isSelected={rule.enabled}
                    onChange={(enabled) => onChange({ ...rule, enabled })}
                />
                <Button
                    isIconOnly
                    aria-label={`Edit ${rule.name}`}
                    size='sm'
                    variant='ghost'
                    onPress={onEdit}
                >
                    <Pencil className='h-4 w-4' />
                </Button>
                <Tooltip>
                    <Tooltip.Trigger>
                        <Button
                            isIconOnly
                            aria-label={`Edit ${rule.name} as DSL`}
                            size='sm'
                            variant='ghost'
                            onPress={onEditDSL}
                        >
                            <Braces className='h-4 w-4' />
                        </Button>
                    </Tooltip.Trigger>
                    <Tooltip.Content>Edit as DSL</Tooltip.Content>
                </Tooltip>
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
        </div>
    );
}

function GroupEditor({
    group,
    onChange,
    onAddCondition,
    onEditCondition,
}: {
    group: WAFConditionGroup;
    onChange: (group: WAFConditionGroup) => void;
    onAddCondition: () => void;
    onEditCondition: (index: number) => void;
}) {
    const sensors = useSensors(
        useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
        useSensor(TouchSensor, { activationConstraint: { delay: 180, tolerance: 5 } }),
        useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
    );
    const reorder = ({ active, over }: DragEndEvent) => {
        if (!over || active.id === over.id) return;
        const from = group.conditions.findIndex((condition) => condition.id === active.id);
        const to = group.conditions.findIndex((condition) => condition.id === over.id);
        if (from >= 0 && to >= 0)
            onChange({ ...group, conditions: arrayMove(group.conditions, from, to) });
    };
    return (
        <div className='space-y-4'>
            <div className='flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
                <SelectField
                    label='Conditions in this group'
                    options={[
                        { id: 'AND', label: 'Match every condition (AND)' },
                        { id: 'OR', label: 'Match any condition (OR)' },
                    ]}
                    value={group.operator}
                    variant='secondary'
                    onChange={(operator) =>
                        onChange({ ...group, operator: operator as WAFConditionGroup['operator'] })
                    }
                />
                <Button size='sm' variant='secondary' onPress={onAddCondition}>
                    <Plus className='h-4 w-4' />
                    Add condition
                </Button>
            </div>
            <div className='overflow-hidden rounded-lg border border-border'>
                {group.conditions.length === 0 ? (
                    <div className='px-4 py-8 text-center text-sm text-muted'>
                        No conditions in this group.
                    </div>
                ) : (
                    <DndContext
                        collisionDetection={closestCenter}
                        sensors={sensors}
                        onDragEnd={reorder}
                    >
                        <SortableContext
                            items={group.conditions.map((condition) => condition.id)}
                            strategy={verticalListSortingStrategy}
                        >
                            {group.conditions.map((condition, index) => (
                                <SortablePreviewRow
                                    key={condition.id}
                                    badges={[
                                        ...(condition.negate ? ['Negated'] : []),
                                        ...(condition.case_sensitive ? ['Case sensitive'] : []),
                                    ]}
                                    detail={conditionSummary(condition)}
                                    id={condition.id}
                                    meta={
                                        operators.find(([id]) => id === condition.operator)?.[1] ??
                                        condition.operator
                                    }
                                    title={
                                        fields.find(([id]) => id === condition.field)?.[1] ??
                                        condition.field
                                    }
                                    onEdit={() => onEditCondition(index)}
                                    onRemove={() =>
                                        onChange({
                                            ...group,
                                            conditions: group.conditions.filter(
                                                (_, current) => current !== index
                                            ),
                                        })
                                    }
                                />
                            ))}
                        </SortableContext>
                    </DndContext>
                )}
            </div>
        </div>
    );
}

function RuleEditorDialog({
    rule,
    ruleIndex,
    onChange,
    onClose,
    onEditGroupDSL,
    onEditRuleDSL,
    onSave,
}: {
    rule: WAFRule | null;
    ruleIndex: number | null;
    onChange: (rule: WAFRule) => void;
    onClose: () => void;
    onEditGroupDSL: (index: number) => void;
    onEditRuleDSL: () => void;
    onSave: () => void;
}) {
    const [groupDraft, setGroupDraft] = useState<WAFConditionGroup | null>(null);
    const [groupIndex, setGroupIndex] = useState<number | null>(null);
    const [conditionDraft, setConditionDraft] = useState<WAFCondition | null>(null);
    const [conditionIndex, setConditionIndex] = useState<number | null>(null);

    useEffect(() => {
        if (!rule) {
            setGroupDraft(null);
            setGroupIndex(null);
            setConditionDraft(null);
            setConditionIndex(null);
        }
    }, [rule]);

    const openGroup = (index: number | null) => {
        if (!rule) return;
        const next =
            index === null
                ? { ...newConditionGroup(), conditions: [] }
                : structuredClone(rule.conditions.groups[index]);
        setGroupDraft(next);
        setGroupIndex(index);
    };
    const openCondition = (index: number | null) => {
        if (!groupDraft) return;
        setConditionDraft(
            index === null ? newCondition() : structuredClone(groupDraft.conditions[index])
        );
        setConditionIndex(index);
    };
    const closeGroup = () => {
        setGroupDraft(null);
        setGroupIndex(null);
    };
    const closeCondition = () => {
        setConditionDraft(null);
        setConditionIndex(null);
    };
    const saveCondition = () => {
        if (!groupDraft || !conditionDraft) return;
        setGroupDraft({
            ...groupDraft,
            conditions:
                conditionIndex === null
                    ? [...groupDraft.conditions, conditionDraft]
                    : groupDraft.conditions.map((condition, index) =>
                          index === conditionIndex ? conditionDraft : condition
                      ),
        });
        closeCondition();
    };
    const saveGroup = () => {
        if (!rule || !groupDraft) return;
        onChange({
            ...rule,
            conditions: {
                ...rule.conditions,
                groups:
                    groupIndex === null
                        ? [...rule.conditions.groups, groupDraft]
                        : rule.conditions.groups.map((group, index) =>
                              index === groupIndex ? groupDraft : group
                          ),
            },
        });
        closeGroup();
    };

    const level = conditionDraft ? 'condition' : groupDraft ? 'group' : 'rule';
    const groupIsValid = Boolean(
        groupDraft?.conditions.length && groupDraft.conditions.every(conditionValid)
    );
    return (
        <DialogShell
            clusterContext='none'
            isOpen={Boolean(rule)}
            size='xl'
            subtitle={
                level === 'condition'
                    ? conditionIndex === null
                        ? `New condition in group ${(groupIndex ?? 0) + 1}`
                        : `Condition ${conditionIndex + 1} in group ${(groupIndex ?? 0) + 1}`
                    : level === 'group'
                      ? `${groupDraft?.conditions.length ?? 0} condition${groupDraft?.conditions.length === 1 ? '' : 's'} · ${groupDraft?.operator ?? 'AND'}`
                      : 'Configure matching logic and the action to execute.'
            }
            title={
                level === 'condition'
                    ? conditionIndex === null
                        ? 'Add condition'
                        : 'Edit condition'
                    : level === 'group'
                      ? groupIndex === null
                          ? 'Add condition group'
                          : `Edit condition group ${groupIndex + 1}`
                      : ruleIndex === null
                        ? 'Add rule'
                        : 'Edit rule'
            }
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
        >
            <div className='max-h-[calc(100dvh-12rem)] overflow-y-auto px-4 py-5 sm:px-6'>
                {level !== 'rule' && (
                    <Button
                        className='mb-4'
                        size='sm'
                        variant='ghost'
                        onPress={level === 'condition' ? closeCondition : closeGroup}
                    >
                        <ArrowLeft className='h-4 w-4' />
                        {level === 'condition' ? 'Back to condition group' : 'Back to rule'}
                    </Button>
                )}
                {conditionDraft ? (
                    <ConditionEditor condition={conditionDraft} onChange={setConditionDraft} />
                ) : groupDraft ? (
                    <GroupEditor
                        group={groupDraft}
                        onAddCondition={() => openCondition(null)}
                        onChange={setGroupDraft}
                        onEditCondition={openCondition}
                    />
                ) : rule ? (
                    <div className='space-y-4'>
                        <div className='flex justify-end'>
                            <Button size='sm' variant='secondary' onPress={onEditRuleDSL}>
                                <Braces className='h-4 w-4' />
                                Edit rule DSL
                            </Button>
                        </div>
                        <RuleEditor
                            rule={rule}
                            onAddGroup={() => openGroup(null)}
                            onChange={onChange}
                            onEditGroup={openGroup}
                            onEditGroupDSL={onEditGroupDSL}
                        />
                    </div>
                ) : null}
            </div>
            <DialogFooter>
                <Button
                    variant='ghost'
                    onPress={
                        level === 'condition'
                            ? closeCondition
                            : level === 'group'
                              ? closeGroup
                              : onClose
                    }
                >
                    {level === 'rule' ? 'Cancel' : 'Back'}
                </Button>
                <Button
                    isDisabled={
                        level === 'condition'
                            ? !conditionDraft || !conditionValid(conditionDraft)
                            : level === 'group'
                              ? !groupIsValid
                              : !rule || !ruleValid(rule)
                    }
                    variant='primary'
                    onPress={
                        level === 'condition'
                            ? saveCondition
                            : level === 'group'
                              ? saveGroup
                              : onSave
                    }
                >
                    {level === 'condition'
                        ? conditionIndex === null
                            ? 'Add condition'
                            : 'Save condition'
                        : level === 'group'
                          ? groupIndex === null
                              ? 'Add group'
                              : 'Save group'
                          : ruleIndex === null
                            ? 'Add rule'
                            : 'Save rule'}
                </Button>
            </DialogFooter>
        </DialogShell>
    );
}

function SortableRuleSet({
    ruleSet,
    waf,
    onChange,
    onOpenDSL,
    onRemove,
    onWAFChange,
}: {
    ruleSet: WAFRuleSet;
    waf: SecurityPolicy['waf'];
    onChange: (ruleSet: WAFRuleSet) => void;
    onOpenDSL: (request: WAFDSLEditRequest) => void;
    onRemove: () => void;
    onWAFChange: (waf: SecurityPolicy['waf']) => void;
}) {
    const [expanded, setExpanded] = useState(true);
    const [ruleDraft, setRuleDraft] = useState<WAFRule | null>(null);
    const [ruleIndex, setRuleIndex] = useState<number | null>(null);
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
    const openRule = (index: number | null) => {
        setRuleDraft(index === null ? newRule() : structuredClone(ruleSet.rules[index]));
        setRuleIndex(index);
    };
    const closeRule = () => {
        setRuleDraft(null);
        setRuleIndex(null);
    };
    const saveRule = () => {
        if (!ruleDraft) return;
        onChange({
            ...ruleSet,
            rules:
                ruleIndex === null
                    ? [...ruleSet.rules, ruleDraft]
                    : ruleSet.rules.map((rule, index) => (index === ruleIndex ? ruleDraft : rule)),
        });
        closeRule();
    };
    const draftPolicy = (draft: WAFRule) => {
        const next = structuredClone(waf);
        const set = next.rule_sets.find((item) => item.id === ruleSet.id);
        if (!set) return next;
        if (ruleIndex === null) set.rules.push(draft);
        else set.rules[ruleIndex] = draft;
        return next;
    };
    const openRuleDSL = (rule: WAFRule) =>
        onOpenDSL({
            title: 'Edit rule DSL',
            subtitle: `${ruleSet.name} / ${rule.name}`,
            request: {
                scope: 'RULE',
                target: { rule_set_id: ruleSet.id, rule_id: rule.id },
                waf,
            },
            onApply: onWAFChange,
        });
    const openDraftRuleDSL = () => {
        if (!ruleDraft) return;
        const snapshot = draftPolicy(ruleDraft);
        const position = ruleIndex ?? ruleSet.rules.length;
        onOpenDSL({
            title: 'Edit rule DSL',
            subtitle: `${ruleSet.name} / ${ruleDraft.name}`,
            request: {
                scope: 'RULE',
                target: { rule_set_id: ruleSet.id, rule_id: ruleDraft.id },
                waf: snapshot,
            },
            onApply: (next) => {
                const set = next.rule_sets.find((item) => item.id === ruleSet.id);
                if (set?.rules[position]) setRuleDraft(structuredClone(set.rules[position]));
            },
        });
    };
    const openDraftGroupDSL = (groupIndex: number) => {
        if (!ruleDraft) return;
        const snapshot = draftPolicy(ruleDraft);
        const position = ruleIndex ?? ruleSet.rules.length;
        const group = ruleDraft.conditions.groups[groupIndex];
        onOpenDSL({
            title: 'Edit condition group DSL',
            subtitle: `${ruleDraft.name} / Group ${groupIndex + 1}`,
            request: {
                scope: 'GROUP',
                target: {
                    rule_set_id: ruleSet.id,
                    rule_id: ruleDraft.id,
                    group_id: group.id,
                },
                waf: snapshot,
            },
            onApply: (next) => {
                const set = next.rule_sets.find((item) => item.id === ruleSet.id);
                if (set?.rules[position]) setRuleDraft(structuredClone(set.rules[position]));
            },
        });
    };
    return (
        <>
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
                    <Button size='sm' variant='secondary' onPress={() => openRule(null)}>
                        <Plus className='h-4 w-4' />
                        Add rule
                    </Button>
                    <Tooltip>
                        <Tooltip.Trigger>
                            <Button
                                isIconOnly
                                aria-label={`Edit ${ruleSet.name} as DSL`}
                                size='sm'
                                variant='ghost'
                                onPress={() =>
                                    onOpenDSL({
                                        title: 'Edit rule set DSL',
                                        subtitle: ruleSet.name,
                                        request: {
                                            scope: 'RULE_SET',
                                            target: { rule_set_id: ruleSet.id },
                                            waf,
                                        },
                                        onApply: onWAFChange,
                                    })
                                }
                            >
                                <Braces className='h-4 w-4' />
                            </Button>
                        </Tooltip.Trigger>
                        <Tooltip.Content>Edit rule set as DSL</Tooltip.Content>
                    </Tooltip>
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
                                        onEdit={() => openRule(index)}
                                        onEditDSL={() => openRuleDSL(rule)}
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
            <RuleEditorDialog
                rule={ruleDraft}
                ruleIndex={ruleIndex}
                onChange={setRuleDraft}
                onClose={closeRule}
                onEditGroupDSL={openDraftGroupDSL}
                onEditRuleDSL={openDraftRuleDSL}
                onSave={saveRule}
            />
        </>
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
    renderDSL,
    validateDSL,
    onChange,
    onDiscard,
    onSave,
}: {
    policy: SecurityPolicy;
    isDirty: boolean;
    saving: boolean;
    renderDSL: (request: WAFDSLRequest) => Promise<WAFDSLRenderResponse>;
    validateDSL: (request: WAFDSLRequest) => Promise<WAFDSLValidationResponse>;
    onChange: (policy: SecurityPolicy) => void;
    onDiscard: () => void;
    onSave: () => void;
}) {
    const [dslEdit, setDSLEdit] = useState<WAFDSLEditRequest | null>(null);
    const sensors = useSensors(
        useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
        useSensor(TouchSensor, { activationConstraint: { delay: 180, tolerance: 5 } }),
        useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
    );
    const valid = useMemo(
        () =>
            policy.waf.rule_sets.every(
                (set) => set.id.trim() && set.name.trim() && set.rules.every(ruleValid)
            ),
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
        <div className='space-y-4'>
            <ContentCard noPadding>
                <div className='flex flex-col gap-3 border-b border-border px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                    <div className='flex items-center gap-2'>
                        <ShieldCheck className='h-4 w-4 text-primary' />
                        <h2 className='text-sm font-semibold'>Web application firewall</h2>
                    </div>
                    <div className='flex flex-wrap items-center gap-3'>
                        <ToggleSwitch
                            label='Enable WAF'
                            isSelected={policy.waf.enabled}
                            onChange={(enabled) =>
                                onChange({ ...policy, waf: { ...policy.waf, enabled } })
                            }
                        />
                        <Button
                            variant='secondary'
                            onPress={() => updateSets([newRuleSet(), ...policy.waf.rule_sets])}
                        >
                            <Plus className='h-4 w-4' />
                            Add rule set
                        </Button>
                        <Tooltip>
                            <Tooltip.Trigger>
                                <Button
                                    isIconOnly
                                    aria-label='Edit WAF policy as DSL'
                                    variant='ghost'
                                    onPress={() =>
                                        setDSLEdit({
                                            title: 'Edit WAF policy DSL',
                                            subtitle: 'Current site policy',
                                            request: {
                                                scope: 'POLICY',
                                                target: {},
                                                waf: policy.waf,
                                            },
                                            onApply: (waf) => onChange({ ...policy, waf }),
                                        })
                                    }
                                >
                                    <Braces className='h-4 w-4' />
                                </Button>
                            </Tooltip.Trigger>
                            <Tooltip.Content>Edit policy as DSL</Tooltip.Content>
                        </Tooltip>
                    </div>
                </div>
                <div className='space-y-4 p-5'>
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
                                            waf={policy.waf}
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
                                            onOpenDSL={setDSLEdit}
                                            onWAFChange={(waf) => onChange({ ...policy, waf })}
                                        />
                                    ))
                                )}
                            </div>
                        </SortableContext>
                    </DndContext>
                </div>
            </ContentCard>
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
            {dslEdit && (
                <Suspense fallback={null}>
                    <WAFDSLEditorDialog
                        renderDSL={renderDSL}
                        validateDSL={validateDSL}
                        value={dslEdit}
                        onClose={() => setDSLEdit(null)}
                    />
                </Suspense>
            )}
        </div>
    );
}
