import type { NotificationChannel } from '@/api';

import { Alert, Button, Input, Tooltip } from '@heroui/react';
import { BellRing, Loader2, Pencil, Plus, Send, Trash2 } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, clusterApi } from '@/api';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { useCluster } from '@/hooks/useCluster.ts';
import {
    anyFieldFilled,
    customTemplateId,
    findNotificationTemplate,
    notificationTemplates,
    requiredFieldsFilled,
} from '@/utils/notificationTemplates.ts';
import { canManageCluster } from '@/utils/rbac.ts';

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

interface TestResult {
    ok: boolean;
    message: string;
}

export default function Notifications() {
    const { clusterId, clusters, ready } = useCluster();
    const api = useMemo(() => clusterApi(clusterId), [clusterId]);
    const canManage = canManageCluster(clusters.find((cluster) => cluster.id === clusterId)?.role);
    const [channels, setChannels] = useState<NotificationChannel[]>([]);
    const [loading, setLoading] = useState(false);
    const [busyID, setBusyID] = useState('');
    const [error, setError] = useState('');
    const [success, setSuccess] = useState('');
    const [dialogOpen, setDialogOpen] = useState(false);
    const [editing, setEditing] = useState<NotificationChannel | null>(null);
    const [name, setName] = useState('');
    const [enabled, setEnabled] = useState(true);
    const [mode, setMode] = useState(notificationTemplates[0].id);
    const [customURL, setCustomURL] = useState('');
    const [values, setValues] = useState<Record<string, string>>({});
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState<TestResult | null>(null);
    const [deleteTarget, setDeleteTarget] = useState<NotificationChannel | null>(null);

    const activeTemplate = mode === customTemplateId ? undefined : findNotificationTemplate(mode);
    const fieldsComplete = activeTemplate ? requiredFieldsFilled(activeTemplate, values) : false;
    const composedURL =
        mode === customTemplateId
            ? customURL.trim()
            : activeTemplate && fieldsComplete
              ? activeTemplate.build(values)
              : '';
    const templateInvalid = Boolean(activeTemplate && fieldsComplete && composedURL === '');

    const load = useCallback(async () => {
        if (!clusterId) {
            setChannels([]);
            return;
        }
        setLoading(true);
        setError('');
        try {
            setChannels(await api.notificationChannels());
        } catch (loadError) {
            setError(errorMessage(loadError, 'Failed to load notification channels'));
        } finally {
            setLoading(false);
        }
    }, [api, clusterId]);

    useEffect(() => {
        if (ready) void load();
    }, [load, ready]);

    const resetDialog = () => {
        setError('');
        setTestResult(null);
        setValues({});
        setCustomURL('');
    };

    const openCreate = () => {
        setEditing(null);
        setName('');
        setEnabled(true);
        setMode(notificationTemplates[0].id);
        resetDialog();
        setDialogOpen(true);
    };

    const openEdit = (channel: NotificationChannel) => {
        setEditing(channel);
        setName(channel.name);
        setEnabled(channel.enabled);
        setMode(findNotificationTemplate(channel.service)?.id ?? customTemplateId);
        resetDialog();
        setDialogOpen(true);
    };

    const changeMode = (next: string) => {
        setMode(next);
        setValues({});
        setCustomURL('');
        setTestResult(null);
        setError('');
    };

    const validateDestination = () => {
        if (activeTemplate) {
            if (anyFieldFilled(activeTemplate, values) && !fieldsComplete) {
                return 'Fill in all required destination fields.';
            }
            if (templateInvalid) {
                return `The ${activeTemplate.label} details are invalid; check the format and try again.`;
            }
        }
        if (!editing && !composedURL) {
            return mode === customTemplateId
                ? 'Enter a Shoutrrr URL.'
                : 'Fill in the destination details.';
        }
        return '';
    };

    const save = async (event: React.FormEvent) => {
        event.preventDefault();
        const validationError = validateDestination();
        if (validationError) {
            setError(validationError);
            return;
        }
        setBusyID(editing?.id ?? 'create');
        setError('');
        setSuccess('');
        try {
            const payload = { name, url: composedURL, enabled };
            if (editing) {
                await api.updateNotificationChannel(editing.id, payload);
            } else {
                await api.createNotificationChannel(payload);
            }
            setDialogOpen(false);
            setSuccess(editing ? 'Notification channel updated.' : 'Notification channel added.');
            await load();
        } catch (saveError) {
            setError(errorMessage(saveError, 'Failed to save notification channel'));
        } finally {
            setBusyID('');
        }
    };

    const testInDialog = async () => {
        const validationError = validateDestination();
        if (validationError) {
            setError(validationError);
            return;
        }
        setTesting(true);
        setError('');
        setTestResult(null);
        try {
            if (composedURL) {
                await api.testDraftNotificationChannel({ url: composedURL, name });
            } else if (editing) {
                await api.testNotificationChannel(editing.id);
            } else {
                return;
            }
            setTestResult({ ok: true, message: 'Test notification delivered.' });
        } catch (testError) {
            setTestResult({
                ok: false,
                message: errorMessage(testError, 'Failed to deliver test notification'),
            });
        } finally {
            setTesting(false);
        }
    };

    const test = async (channel: NotificationChannel) => {
        setBusyID(channel.id);
        setError('');
        setSuccess('');
        try {
            await api.testNotificationChannel(channel.id);
            setSuccess(`Test notification delivered through ${channel.name}.`);
        } catch (testError) {
            setError(errorMessage(testError, 'Failed to deliver test notification'));
        } finally {
            setBusyID('');
        }
    };

    const remove = async () => {
        if (!deleteTarget) return;
        setBusyID(deleteTarget.id);
        setError('');
        try {
            await api.deleteNotificationChannel(deleteTarget.id);
            setDeleteTarget(null);
            setSuccess('Notification channel deleted.');
            await load();
        } catch (deleteError) {
            setError(errorMessage(deleteError, 'Failed to delete notification channel'));
        } finally {
            setBusyID('');
        }
    };

    return (
        <div className='space-y-5'>
            <PageHeader
                actions={
                    <Button isDisabled={!clusterId || !canManage} onPress={openCreate}>
                        <Plus aria-hidden='true' className='h-4 w-4' />
                        Add channel
                    </Button>
                }
                subtitle='Manage alert destinations for the selected cluster.'
                title='Notifications'
            />

            {error && !dialogOpen && <FormError message={error} />}
            {success && (
                <Alert status='success'>
                    <Alert.Indicator />
                    <Alert.Content>
                        <Alert.Title>Notification updated</Alert.Title>
                        <Alert.Description>{success}</Alert.Description>
                    </Alert.Content>
                </Alert>
            )}

            <ContentCard noPadding title='Notification channels'>
                {!clusterId ? (
                    <div className='px-5 py-10 text-center text-sm text-muted'>
                        Select a cluster to manage notification channels.
                    </div>
                ) : loading ? (
                    <div className='flex items-center justify-center gap-2 px-5 py-10 text-sm text-muted'>
                        <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                        Loading channels…
                    </div>
                ) : channels.length === 0 ? (
                    <div className='px-5 py-10 text-center'>
                        <BellRing aria-hidden='true' className='mx-auto h-6 w-6 text-muted' />
                        <div className='mt-3 text-sm font-medium'>No notification channels</div>
                        <div className='mt-1 text-xs text-muted'>
                            Cluster alerts have no delivery destination.
                        </div>
                    </div>
                ) : (
                    <div className='divide-y divide-border'>
                        {channels.map((channel) => (
                            <div
                                key={channel.id}
                                className='flex flex-col gap-3 px-4 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-5'
                            >
                                <div className='min-w-0'>
                                    <div className='flex flex-wrap items-center gap-2'>
                                        <span className='truncate text-sm font-semibold'>
                                            {channel.name}
                                        </span>
                                        <span
                                            className={`text-xs ${channel.enabled ? 'text-success' : 'text-muted'}`}
                                        >
                                            {channel.enabled ? 'Enabled' : 'Disabled'}
                                        </span>
                                    </div>
                                    <div className='mt-1 flex flex-wrap items-center gap-2 text-xs text-muted'>
                                        <span className='uppercase'>{channel.service}</span>
                                        <code className='break-all'>{channel.masked_url}</code>
                                    </div>
                                </div>
                                <div className='flex shrink-0 items-center gap-2'>
                                    {canManage && (
                                        <>
                                            <Button
                                                isDisabled={Boolean(busyID)}
                                                size='sm'
                                                variant='secondary'
                                                onPress={() => void test(channel)}
                                            >
                                                {busyID === channel.id ? (
                                                    <Loader2
                                                        aria-hidden='true'
                                                        className='h-4 w-4 animate-spin'
                                                    />
                                                ) : (
                                                    <Send aria-hidden='true' className='h-4 w-4' />
                                                )}
                                                Test
                                            </Button>
                                            <Tooltip>
                                                <Tooltip.Trigger>
                                                    <Button
                                                        isIconOnly
                                                        aria-label={`Edit ${channel.name}`}
                                                        isDisabled={Boolean(busyID)}
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() => openEdit(channel)}
                                                    >
                                                        <Pencil
                                                            aria-hidden='true'
                                                            className='h-4 w-4'
                                                        />
                                                    </Button>
                                                </Tooltip.Trigger>
                                                <Tooltip.Content>Edit channel</Tooltip.Content>
                                            </Tooltip>
                                            <Tooltip>
                                                <Tooltip.Trigger>
                                                    <Button
                                                        isIconOnly
                                                        aria-label={`Delete ${channel.name}`}
                                                        isDisabled={Boolean(busyID)}
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() => setDeleteTarget(channel)}
                                                    >
                                                        <Trash2
                                                            aria-hidden='true'
                                                            className='h-4 w-4 text-danger'
                                                        />
                                                    </Button>
                                                </Tooltip.Trigger>
                                                <Tooltip.Content>Delete channel</Tooltip.Content>
                                            </Tooltip>
                                        </>
                                    )}
                                </div>
                            </div>
                        ))}
                    </div>
                )}
            </ContentCard>

            <DialogShell
                clusterContext={editing ? 'current' : 'target'}
                icon={<BellRing className='h-5 w-5' />}
                isOpen={dialogOpen}
                size='md'
                subtitle='The destination is encrypted and scoped to this cluster.'
                title={editing ? 'Edit notification channel' : 'Add notification channel'}
                onOpenChange={(open) => {
                    setDialogOpen(open);
                    if (!open) setError('');
                }}
            >
                <form onSubmit={save}>
                    <div className='max-h-[70dvh] space-y-5 overflow-y-auto px-6 py-5'>
                        {error && <FormError message={error} />}
                        <FormField htmlFor='notification-name' label='Name' required>
                            <Input
                                id='notification-name'
                                maxLength={120}
                                value={name}
                                variant='secondary'
                                onChange={(event) => setName(event.target.value)}
                            />
                        </FormField>
                        <SelectField
                            isRequired
                            label='Service'
                            options={[
                                ...notificationTemplates.map((template) => ({
                                    id: template.id,
                                    label: template.label,
                                })),
                                { id: customTemplateId, label: 'Custom URL' },
                            ]}
                            value={mode}
                            variant='secondary'
                            onChange={changeMode}
                        />
                        {activeTemplate ? (
                            <>
                                {activeTemplate.fields.map((field) => (
                                    <FormField
                                        htmlFor={`notification-${field.key}`}
                                        key={field.key}
                                        label={field.label}
                                        required={!field.optional}
                                    >
                                        <Input
                                            autoCapitalize='none'
                                            autoComplete='off'
                                            id={`notification-${field.key}`}
                                            placeholder={field.placeholder}
                                            type={field.secret ? 'password' : 'text'}
                                            value={values[field.key] ?? ''}
                                            variant='secondary'
                                            onChange={(event) => {
                                                setValues({
                                                    ...values,
                                                    [field.key]: event.target.value,
                                                });
                                                setTestResult(null);
                                            }}
                                        />
                                    </FormField>
                                ))}
                                <p className='text-xs text-muted -mt-2'>
                                    <a
                                        className='underline'
                                        href={activeTemplate.docsURL}
                                        rel='noreferrer'
                                        target='_blank'
                                    >
                                        Shoutrrr {activeTemplate.label} documentation
                                    </a>
                                    {editing
                                        ? ' — leave the fields empty to keep the current destination.'
                                        : ''}
                                </p>
                            </>
                        ) : (
                            <FormField
                                hint={
                                    editing
                                        ? 'Leave empty to keep the current destination.'
                                        : 'Any Shoutrrr service URL, e.g. discord://token@webhook-id.'
                                }
                                htmlFor='notification-url'
                                label='Shoutrrr URL'
                                required={!editing}
                            >
                                <Input
                                    autoCapitalize='none'
                                    autoComplete='off'
                                    id='notification-url'
                                    placeholder={
                                        editing ? editing.masked_url : 'service://destination'
                                    }
                                    type='password'
                                    value={customURL}
                                    variant='secondary'
                                    onChange={(event) => {
                                        setCustomURL(event.target.value);
                                        setTestResult(null);
                                    }}
                                />
                            </FormField>
                        )}
                        <div className='flex items-center justify-between gap-3'>
                            <span className='text-sm font-medium'>Enabled</span>
                            <ToggleSwitch
                                isSelected={enabled}
                                label='Enabled'
                                onChange={setEnabled}
                            />
                        </div>
                        {testResult &&
                            (testResult.ok ? (
                                <p className='text-xs text-success'>{testResult.message}</p>
                            ) : (
                                <FormError message={testResult.message} />
                            ))}
                    </div>
                    <DialogFooter>
                        <Button
                            className='mr-auto'
                            isDisabled={testing || Boolean(busyID) || (!composedURL && !editing)}
                            type='button'
                            variant='secondary'
                            onPress={() => void testInDialog()}
                        >
                            {testing ? (
                                <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                            ) : (
                                <Send aria-hidden='true' className='h-4 w-4' />
                            )}
                            Test
                        </Button>
                        <Button type='button' variant='ghost' onPress={() => setDialogOpen(false)}>
                            Cancel
                        </Button>
                        <Button isDisabled={Boolean(busyID) || testing} type='submit'>
                            {busyID && (
                                <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                            )}
                            Save
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <ConfirmDialog
                danger
                confirmLabel='Delete channel'
                description={
                    deleteTarget ? `Delete ${deleteTarget.name} from this cluster?` : undefined
                }
                isOpen={Boolean(deleteTarget)}
                loading={Boolean(busyID)}
                title='Delete notification channel'
                onConfirm={() => void remove()}
                onOpenChange={(open) => {
                    if (!open) setDeleteTarget(null);
                }}
            />
        </div>
    );
}
