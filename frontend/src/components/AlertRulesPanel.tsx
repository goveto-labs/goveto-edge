import type { AlertRule, AlertRuleUpdate, AlertSeverity } from '@/api';

import { Button, Input, Tooltip } from '@heroui/react';
import { BellOff, BellRing, PencilLine, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, alertsApi, clusterApi } from '@/api';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { useCluster } from '@/hooks/useCluster.ts';
import { formatRuleSeconds } from '@/utils/alerts.ts';

interface NotificationChannelOption {
    id: string;
    label: string;
}

const severityOptions = [
    { id: 'CRITICAL', label: 'Critical' },
    { id: 'WARNING', label: 'Warning' },
    { id: 'INFO', label: 'Info' },
];

function isMuted(rule: AlertRule, now: number) {
    return Boolean(rule.mutedUntil && new Date(rule.mutedUntil).getTime() > now);
}

export function AlertRulesPanel({
    canOperate,
    onChanged,
}: {
    canOperate: boolean;
    onChanged: () => void | Promise<void>;
}) {
    const { clusterId } = useCluster();
    const api = useMemo(() => alertsApi(clusterId), [clusterId]);
    const [rules, setRules] = useState<AlertRule[]>([]);
    const [channels, setChannels] = useState<NotificationChannelOption[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [editing, setEditing] = useState<AlertRule | null>(null);
    const [muteNow, setMuteNow] = useState(() => Date.now());
    const requestVersion = useRef(0);

    const load = useCallback(async () => {
        if (!clusterId) return;
        const version = ++requestVersion.current;
        setLoading(true);
        try {
            const [ruleList, channelList] = await Promise.all([
                api.rules(),
                clusterApi(clusterId).notificationChannels(),
            ]);
            if (version !== requestVersion.current) return;
            setRules(ruleList);
            setChannels(channelList.map((channel) => ({ id: channel.id, label: channel.name })));
            setError('');
        } catch (loadError) {
            if (version !== requestVersion.current) return;
            setError(loadError instanceof ApiError ? loadError.message : 'Failed to load rules');
        } finally {
            if (version === requestVersion.current) setLoading(false);
        }
    }, [api, clusterId]);

    useEffect(() => {
        void load();
    }, [load]);

    useEffect(() => {
        const nextExpiry = rules.reduce((next, rule) => {
            const expiry = rule.mutedUntil ? new Date(rule.mutedUntil).getTime() : Number.NaN;
            return expiry > muteNow && expiry < next ? expiry : next;
        }, Number.POSITIVE_INFINITY);
        if (!Number.isFinite(nextExpiry)) return;
        const delay = Math.min(Math.max(0, nextExpiry - Date.now()) + 50, 2_147_483_647);
        const timer = window.setTimeout(() => setMuteNow(Date.now()), delay);
        return () => window.clearTimeout(timer);
    }, [muteNow, rules]);

    const patchRule = useCallback(
        async (rule: AlertRule, body: AlertRuleUpdate) => {
            try {
                await api.updateRule(rule.id, body);
                setError('');
                await load();
                await onChanged();
            } catch (updateError) {
                setError(
                    updateError instanceof ApiError ? updateError.message : 'Failed to update rule'
                );
            }
        },
        [api, load, onChanged]
    );

    return (
        <div className='space-y-4'>
            {error && (
                <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                    {error}
                </div>
            )}
            <DataTable
                action={
                    <Button
                        isIconOnly
                        aria-label='Refresh alert rules'
                        variant='ghost'
                        onPress={() => void load()}
                    >
                        <RefreshCw
                            aria-hidden='true'
                            className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`}
                        />
                    </Button>
                }
                aria-label='Alert rules'
                className='[&_td]:py-3 [&_th]:tracking-normal'
                empty={rules.length === 0}
                emptyDescription='Built-in rules are seeded on the next evaluation cycle.'
                emptyTitle='No alert rules'
                loading={loading && rules.length === 0}
                title={`${rules.length} rules`}
            >
                <thead>
                    <tr>
                        <th>Rule</th>
                        <th>Severity</th>
                        <th>Hold</th>
                        <th>Reminder</th>
                        <th>Channels</th>
                        <th>State</th>
                        <th aria-label='Actions' />
                    </tr>
                </thead>
                <tbody>
                    {rules.map((rule) => {
                        const muted = isMuted(rule, muteNow);
                        return (
                            <tr key={rule.id}>
                                <td className='max-w-96'>
                                    <div className='truncate text-sm font-medium'>{rule.label}</div>
                                    <div
                                        className='mt-0.5 truncate text-xs text-muted'
                                        title={rule.description}
                                    >
                                        {rule.description}
                                    </div>
                                </td>
                                <td className='text-xs'>{rule.severity.toLowerCase()}</td>
                                <td className='text-xs text-muted'>
                                    {formatRuleSeconds(rule.forSeconds)}
                                </td>
                                <td className='text-xs text-muted'>
                                    every {formatRuleSeconds(rule.cooldownSeconds)}
                                </td>
                                <td className='text-xs tabular text-muted'>
                                    {rule.channels.length === 0
                                        ? channels.length === 0
                                            ? 'no channels configured'
                                            : 'all channels'
                                        : `${rule.channels.length} selected`}
                                </td>
                                <td>
                                    <div className='flex flex-col gap-1'>
                                        <span className='text-xs'>
                                            {rule.enabled ? 'Enabled' : 'Disabled'}
                                        </span>
                                        {muted && (
                                            <span className='inline-flex items-center gap-1 text-xs text-warning'>
                                                <BellOff aria-hidden='true' className='h-3 w-3' />
                                                muted
                                            </span>
                                        )}
                                    </div>
                                </td>
                                <td>
                                    <div className='flex justify-end gap-1'>
                                        {canOperate && (
                                            <>
                                                <Tooltip>
                                                    <Tooltip.Trigger>
                                                        <Button
                                                            isIconOnly
                                                            aria-label={`${muted ? 'Unmute' : 'Mute'} ${rule.label}`}
                                                            size='sm'
                                                            variant='ghost'
                                                            onPress={() => {
                                                                if (isMuted(rule, Date.now())) {
                                                                    void patchRule(rule, {
                                                                        unmute: true,
                                                                    });
                                                                } else {
                                                                    const until = new Date(
                                                                        Date.now() + 60 * 60 * 1000
                                                                    ).toISOString();
                                                                    void patchRule(rule, {
                                                                        mutedUntil: until,
                                                                    });
                                                                }
                                                            }}
                                                        >
                                                            {muted ? (
                                                                <BellRing
                                                                    aria-hidden='true'
                                                                    className='h-4 w-4'
                                                                />
                                                            ) : (
                                                                <BellOff
                                                                    aria-hidden='true'
                                                                    className='h-4 w-4'
                                                                />
                                                            )}
                                                        </Button>
                                                    </Tooltip.Trigger>
                                                    <Tooltip.Content>
                                                        {muted ? 'Unmute' : 'Mute for 1 hour'}
                                                    </Tooltip.Content>
                                                </Tooltip>
                                                <Tooltip>
                                                    <Tooltip.Trigger>
                                                        <Button
                                                            isIconOnly
                                                            aria-label={`Edit ${rule.label}`}
                                                            size='sm'
                                                            variant='ghost'
                                                            onPress={() => setEditing(rule)}
                                                        >
                                                            <PencilLine
                                                                aria-hidden='true'
                                                                className='h-4 w-4'
                                                            />
                                                        </Button>
                                                    </Tooltip.Trigger>
                                                    <Tooltip.Content>Edit rule</Tooltip.Content>
                                                </Tooltip>
                                            </>
                                        )}
                                    </div>
                                </td>
                            </tr>
                        );
                    })}
                </tbody>
            </DataTable>
            <RuleEditDialog
                channels={channels}
                onSaved={async () => {
                    await load();
                    await onChanged();
                }}
                rule={editing}
                onClose={() => setEditing(null)}
            />
        </div>
    );
}

function RuleEditDialog({
    channels,
    onSaved,
    onClose,
    rule,
}: {
    channels: NotificationChannelOption[];
    onSaved: () => void | Promise<void>;
    onClose: () => void;
    rule: AlertRule | null;
}) {
    const { clusterId } = useCluster();
    const [editingClusterId, setEditingClusterId] = useState('');
    const [severity, setSeverity] = useState<AlertSeverity>('WARNING');
    const [enabled, setEnabled] = useState(true);
    const [forSeconds, setForSeconds] = useState('0');
    const [cooldownSeconds, setCooldownSeconds] = useState('300');
    const [params, setParams] = useState<Record<string, string>>({});
    const [selectedChannels, setSelectedChannels] = useState<string[]>([]);
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState('');

    useEffect(() => {
        if (!rule) return;
        setSeverity(rule.severity);
        setEnabled(rule.enabled);
        setEditingClusterId(clusterId);
        setForSeconds(String(rule.forSeconds));
        setCooldownSeconds(String(rule.cooldownSeconds));
        setSelectedChannels(rule.channels);
        const next: Record<string, string> = {};
        for (const spec of rule.parameterSpecs) {
            next[spec.key] = String(rule.paramsJson?.[spec.key] ?? spec.default);
        }
        setParams(next);
        setError('');
    }, [clusterId, rule]);

    useEffect(() => {
        if (rule && editingClusterId && clusterId !== editingClusterId) onClose();
    }, [clusterId, editingClusterId, onClose, rule]);

    const save = useCallback(async () => {
        if (!rule) return;
        if (!editingClusterId || clusterId !== editingClusterId) {
            setError('The selected cluster changed. Reopen the rule before saving.');
            return;
        }
        setSaving(true);
        try {
            const hold = Number(forSeconds);
            const cooldown = Number(cooldownSeconds);
            if (!Number.isInteger(hold) || hold < 0 || hold > 86_400) {
                throw new ApiError('Hold seconds must be an integer from 0 to 86400', 400, null);
            }
            if (!Number.isInteger(cooldown) || cooldown < 60 || cooldown > 86_400) {
                throw new ApiError(
                    'Cooldown seconds must be an integer from 60 to 86400',
                    400,
                    null
                );
            }
            const numericParams: Record<string, number | null> = {};
            for (const spec of rule.parameterSpecs) {
                const value = params[spec.key] ?? '';
                if (value.trim() === '') {
                    numericParams[spec.key] = null;
                    continue;
                }
                const parsed = Number(value);
                if (!Number.isFinite(parsed)) {
                    throw new ApiError(`${spec.label} must be a number`, 400, null);
                }
                if (spec.type === 'integer' && !Number.isInteger(parsed)) {
                    throw new ApiError(`${spec.label} must be an integer`, 400, null);
                }
                if (
                    (spec.exclusiveMinimum && parsed <= spec.minimum) ||
                    (!spec.exclusiveMinimum && parsed < spec.minimum)
                ) {
                    throw new ApiError(
                        `${spec.label} must be ${spec.exclusiveMinimum ? 'greater than' : 'at least'} ${spec.minimum}`,
                        400,
                        null
                    );
                }
                if (parsed > spec.maximum) {
                    throw new ApiError(`${spec.label} must be at most ${spec.maximum}`, 400, null);
                }
                numericParams[spec.key] = parsed;
            }
            await alertsApi(editingClusterId).updateRule(rule.id, {
                enabled,
                severity,
                forSeconds: hold,
                cooldownSeconds: cooldown,
                channels: selectedChannels,
                params: numericParams,
            });
            await onSaved();
            onClose();
        } catch (saveError) {
            setError(saveError instanceof ApiError ? saveError.message : 'Failed to save rule');
        } finally {
            setSaving(false);
        }
    }, [
        clusterId,
        editingClusterId,
        enabled,
        params,
        rule,
        selectedChannels,
        severity,
        forSeconds,
        cooldownSeconds,
        onSaved,
        onClose,
    ]);

    if (!rule) return null;

    return (
        <DialogShell
            isOpen
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
            size='md'
            subtitle={rule.description}
            title={`Rule: ${rule.label}`}
        >
            <div className='space-y-4 px-6 py-6'>
                <ToggleSwitch isSelected={enabled} label='Rule enabled' onChange={setEnabled} />
                <FormField label='Severity'>
                    <SelectField
                        ariaLabel='Rule severity'
                        variant='secondary'
                        options={severityOptions}
                        value={severity}
                        onChange={(value) => setSeverity(value as AlertSeverity)}
                    />
                </FormField>
                <div className='grid gap-4 sm:grid-cols-2'>
                    <FormField
                        label='Hold before firing (s)'
                        hint='The condition must persist this long before the alert fires.'
                    >
                        <Input
                            aria-label='Hold seconds'
                            max={86400}
                            min={0}
                            step={1}
                            type='number'
                            value={forSeconds}
                            variant='secondary'
                            onChange={(event) => setForSeconds(event.target.value)}
                        />
                    </FormField>
                    <FormField
                        label='Reminder cooldown (s)'
                        hint='Minimum interval between repeated firing notifications.'
                    >
                        <Input
                            aria-label='Cooldown seconds'
                            max={86400}
                            min={60}
                            step={1}
                            type='number'
                            value={cooldownSeconds}
                            variant='secondary'
                            onChange={(event) => setCooldownSeconds(event.target.value)}
                        />
                    </FormField>
                </div>
                {rule.parameterSpecs.length > 0 && (
                    <div className='grid gap-4 sm:grid-cols-2'>
                        {rule.parameterSpecs.map((spec) => (
                            <FormField
                                key={spec.key}
                                hint={`Default: ${spec.default}`}
                                label={spec.label}
                            >
                                <Input
                                    aria-label={spec.label}
                                    max={spec.maximum}
                                    min={spec.exclusiveMinimum ? undefined : spec.minimum}
                                    step={spec.step || (spec.type === 'integer' ? 1 : 'any')}
                                    type='number'
                                    value={params[spec.key] ?? ''}
                                    variant='secondary'
                                    onChange={(event) =>
                                        setParams((current) => ({
                                            ...current,
                                            [spec.key]: event.target.value,
                                        }))
                                    }
                                />
                            </FormField>
                        ))}
                    </div>
                )}
                <FormField
                    label='Notification channels'
                    hint='Leave empty to deliver through every enabled channel in this cluster.'
                >
                    <div className='space-y-2 rounded-lg border border-border p-3'>
                        {channels.length === 0 ? (
                            <p className='text-sm text-muted'>
                                No notification channels configured for this cluster.
                            </p>
                        ) : (
                            channels.map((channel) => (
                                <label
                                    className='flex cursor-pointer items-center gap-2 text-sm'
                                    key={channel.id}
                                >
                                    <input
                                        checked={selectedChannels.includes(channel.id)}
                                        type='checkbox'
                                        onChange={(event) =>
                                            setSelectedChannels((current) =>
                                                event.target.checked
                                                    ? [...current, channel.id]
                                                    : current.filter((id) => id !== channel.id)
                                            )
                                        }
                                    />
                                    {channel.label}
                                </label>
                            ))
                        )}
                    </div>
                </FormField>
                {isMuted(rule, Date.now()) && (
                    <p className='text-sm text-warning'>
                        Muted until {new Date(rule.mutedUntil!).toLocaleString()}.
                    </p>
                )}
                {error && <FormError message={error} />}
            </div>
            <DialogFooter>
                <Button variant='ghost' onPress={onClose}>
                    Cancel
                </Button>
                <Button isDisabled={saving} variant='primary' onPress={() => void save()}>
                    Save rule
                </Button>
            </DialogFooter>
        </DialogShell>
    );
}
