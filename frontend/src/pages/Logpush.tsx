import type { LogpushDestination, LogpushLogType } from '@/api';

import { Alert, Button, Input, Tooltip } from '@heroui/react';
import { Loader2, Pencil, Plus, Send, Trash2, Waves } from 'lucide-react';
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
import { canManageCluster } from '@/utils/rbac.ts';

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

interface TestResult {
    ok: boolean;
    message: string;
}

const logTypeOptions: { id: LogpushLogType; label: string }[] = [
    { id: 'access', label: 'Access logs' },
    { id: 'caddy', label: 'Caddy runtime logs' },
    { id: 'node_runtime', label: 'Node runtime metrics' },
    { id: 'origin_health', label: 'Origin health metrics' },
];

const saslOptions = [
    { id: 'none', label: 'None' },
    { id: 'plain', label: 'PLAIN' },
    { id: 'scram-sha-256', label: 'SCRAM-SHA-256' },
    { id: 'scram-sha-512', label: 'SCRAM-SHA-512' },
];

export default function Logpush() {
    const { clusterId, clusters, ready } = useCluster();
    const api = useMemo(() => clusterApi(clusterId), [clusterId]);
    const canManage = canManageCluster(clusters.find((cluster) => cluster.id === clusterId)?.role);
    const [destinations, setDestinations] = useState<LogpushDestination[]>([]);
    const [loading, setLoading] = useState(false);
    const [busyID, setBusyID] = useState('');
    const [error, setError] = useState('');
    const [success, setSuccess] = useState('');
    const [dialogOpen, setDialogOpen] = useState(false);
    const [editing, setEditing] = useState<LogpushDestination | null>(null);
    const [name, setName] = useState('');
    const [brokers, setBrokers] = useState('');
    const [topic, setTopic] = useState('');
    const [logTypes, setLogTypes] = useState<LogpushLogType[]>(['access']);
    const [tlsEnabled, setTlsEnabled] = useState(true);
    const [saslMechanism, setSaslMechanism] = useState('none');
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [enabled, setEnabled] = useState(true);
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState<TestResult | null>(null);
    const [deleteTarget, setDeleteTarget] = useState<LogpushDestination | null>(null);

    const load = useCallback(async () => {
        if (!clusterId) {
            setDestinations([]);
            return;
        }
        setLoading(true);
        setError('');
        try {
            setDestinations(await api.logpushDestinations());
        } catch (loadError) {
            setError(errorMessage(loadError, 'Failed to load logpush destinations'));
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
        setUsername('');
        setPassword('');
    };

    const openCreate = () => {
        setEditing(null);
        setName('');
        setBrokers('');
        setTopic('');
        setLogTypes(['access']);
        setTlsEnabled(true);
        setSaslMechanism('none');
        setEnabled(true);
        resetDialog();
        setDialogOpen(true);
    };

    const openEdit = (destination: LogpushDestination) => {
        setEditing(destination);
        setName(destination.name);
        setBrokers(destination.brokers.join(', '));
        setTopic(destination.topic);
        setLogTypes(destination.log_types);
        setTlsEnabled(destination.tls_enabled);
        setSaslMechanism(destination.sasl_mechanism || 'none');
        setEnabled(destination.enabled);
        resetDialog();
        setDialogOpen(true);
    };

    const toggleLogType = (logType: LogpushLogType, selected: boolean) => {
        setTestResult(null);
        setLogTypes((current) =>
            selected ? [...current, logType] : current.filter((item) => item !== logType)
        );
    };

    const buildPayload = () => ({
        name: name.trim(),
        brokers: brokers.trim(),
        topic: topic.trim(),
        log_types: logTypes,
        tls_enabled: tlsEnabled,
        sasl_mechanism: saslMechanism,
        enabled,
        ...(username.trim() || password ? { username: username.trim(), password } : {}),
    });

    const enteredCredentials = Boolean(username.trim() && password);
    const storedConnectionUnchanged = Boolean(
        editing &&
            brokers
                .split(',')
                .map((broker) => broker.trim())
                .filter(Boolean)
                .join(',') === editing.brokers.join(',') &&
            tlsEnabled === editing.tls_enabled &&
            saslMechanism === (editing.sasl_mechanism || 'none')
    );

    const validateForm = () => {
        if (!name.trim()) return 'Enter a name.';
        if (!editing && !brokers.trim()) return 'Enter at least one Kafka broker (host:port).';
        if (!editing && !topic.trim()) return 'Enter a topic.';
        if (logTypes.length === 0) return 'Select at least one log type.';
        if (saslMechanism !== 'none' && !enteredCredentials && !editing?.credentials_configured) {
            return 'SASL authentication requires a username and password.';
        }
        if ((username.trim() && !password) || (!username.trim() && password)) {
            return 'Username and password must be provided together.';
        }
        return '';
    };

    const save = async (event: React.FormEvent) => {
        event.preventDefault();
        const validationError = validateForm();
        if (validationError) {
            setError(validationError);
            return;
        }
        setBusyID(editing?.id ?? 'create');
        setError('');
        setSuccess('');
        try {
            if (editing) {
                await api.updateLogpushDestination(editing.id, buildPayload());
            } else {
                await api.createLogpushDestination(buildPayload());
            }
            setDialogOpen(false);
            setSuccess(editing ? 'Logpush destination updated.' : 'Logpush destination added.');
            await load();
        } catch (saveError) {
            setError(errorMessage(saveError, 'Failed to save logpush destination'));
        } finally {
            setBusyID('');
        }
    };

    const testInDialog = async () => {
        const validationError = validateForm();
        if (validationError) {
            setError(validationError);
            return;
        }
        const useStoredCredentials = Boolean(
            editing && saslMechanism !== 'none' && !enteredCredentials
        );
        if (useStoredCredentials && !storedConnectionUnchanged) {
            setError('Re-enter the SASL credentials to test changed connection settings.');
            return;
        }
        setTesting(true);
        setError('');
        setTestResult(null);
        try {
            if (useStoredCredentials && editing) {
                await api.testLogpushDestination(editing.id);
                setTestResult({
                    ok: true,
                    message: 'Connected using the stored destination settings.',
                });
            } else {
                await api.testDraftLogpushDestination(buildPayload());
                setTestResult({ ok: true, message: 'Connected to the Kafka brokers.' });
            }
        } catch (testError) {
            setTestResult({
                ok: false,
                message: errorMessage(testError, 'Failed to connect to the Kafka brokers'),
            });
        } finally {
            setTesting(false);
        }
    };

    const test = async (destination: LogpushDestination) => {
        setBusyID(destination.id);
        setError('');
        setSuccess('');
        try {
            await api.testLogpushDestination(destination.id);
            setSuccess(`Connected to the Kafka brokers of ${destination.name}.`);
        } catch (testError) {
            setError(errorMessage(testError, 'Failed to connect to the Kafka brokers'));
        } finally {
            setBusyID('');
        }
    };

    const remove = async () => {
        if (!deleteTarget) return;
        setBusyID(deleteTarget.id);
        setError('');
        try {
            await api.deleteLogpushDestination(deleteTarget.id);
            setDeleteTarget(null);
            setSuccess('Logpush destination deleted.');
            await load();
        } catch (deleteError) {
            setError(errorMessage(deleteError, 'Failed to delete logpush destination'));
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
                        Add destination
                    </Button>
                }
                subtitle='Stream edge logs to external systems such as Kafka.'
                title='Logpush'
            />

            {error && !dialogOpen && <FormError message={error} />}
            {success && (
                <Alert status='success'>
                    <Alert.Indicator />
                    <Alert.Content>
                        <Alert.Title>Logpush updated</Alert.Title>
                        <Alert.Description>{success}</Alert.Description>
                    </Alert.Content>
                </Alert>
            )}

            <ContentCard noPadding title='Logpush destinations'>
                {!clusterId ? (
                    <div className='px-5 py-10 text-center text-sm text-muted'>
                        Select a cluster to manage logpush destinations.
                    </div>
                ) : loading ? (
                    <div className='flex items-center justify-center gap-2 px-5 py-10 text-sm text-muted'>
                        <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                        Loading destinations…
                    </div>
                ) : destinations.length === 0 ? (
                    <div className='px-5 py-10 text-center'>
                        <Waves aria-hidden='true' className='mx-auto h-6 w-6 text-muted' />
                        <div className='mt-3 text-sm font-medium'>No logpush destinations</div>
                        <div className='mt-1 text-xs text-muted'>
                            Edge logs are only stored in the built-in analytics database.
                        </div>
                    </div>
                ) : (
                    <div className='divide-y divide-border'>
                        {destinations.map((destination) => (
                            <div
                                key={destination.id}
                                className='flex flex-col gap-3 px-4 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-5'
                            >
                                <div className='min-w-0'>
                                    <div className='flex flex-wrap items-center gap-2'>
                                        <span className='truncate text-sm font-semibold'>
                                            {destination.name}
                                        </span>
                                        <span
                                            className={`text-xs ${destination.enabled ? 'text-success' : 'text-muted'}`}
                                        >
                                            {destination.enabled ? 'Enabled' : 'Disabled'}
                                        </span>
                                    </div>
                                    <div className='mt-1 flex flex-wrap items-center gap-2 text-xs text-muted'>
                                        <span className='uppercase'>{destination.type}</span>
                                        <code className='break-all'>
                                            {destination.brokers.join(', ')} → {destination.topic}
                                        </code>
                                        <span>{destination.log_types.join(', ')}</span>
                                        {destination.credentials_configured && (
                                            <span>SASL {destination.sasl_mechanism}</span>
                                        )}
                                    </div>
                                </div>
                                <div className='flex shrink-0 items-center gap-2'>
                                    {canManage && (
                                        <>
                                            <Button
                                                isDisabled={Boolean(busyID)}
                                                size='sm'
                                                variant='secondary'
                                                onPress={() => void test(destination)}
                                            >
                                                {busyID === destination.id ? (
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
                                                        aria-label={`Edit ${destination.name}`}
                                                        isDisabled={Boolean(busyID)}
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() => openEdit(destination)}
                                                    >
                                                        <Pencil
                                                            aria-hidden='true'
                                                            className='h-4 w-4'
                                                        />
                                                    </Button>
                                                </Tooltip.Trigger>
                                                <Tooltip.Content>Edit destination</Tooltip.Content>
                                            </Tooltip>
                                            <Tooltip>
                                                <Tooltip.Trigger>
                                                    <Button
                                                        isIconOnly
                                                        aria-label={`Delete ${destination.name}`}
                                                        isDisabled={Boolean(busyID)}
                                                        size='sm'
                                                        variant='ghost'
                                                        onPress={() => setDeleteTarget(destination)}
                                                    >
                                                        <Trash2
                                                            aria-hidden='true'
                                                            className='h-4 w-4 text-danger'
                                                        />
                                                    </Button>
                                                </Tooltip.Trigger>
                                                <Tooltip.Content>
                                                    Delete destination
                                                </Tooltip.Content>
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
                icon={<Waves className='h-5 w-5' />}
                isOpen={dialogOpen}
                size='md'
                subtitle='Credentials are encrypted and scoped to this cluster. Delivery is best-effort.'
                title={editing ? 'Edit logpush destination' : 'Add logpush destination'}
                onOpenChange={(open) => {
                    setDialogOpen(open);
                    if (!open) setError('');
                }}
            >
                <form onSubmit={save}>
                    <div className='max-h-[70dvh] space-y-5 overflow-y-auto px-6 py-5'>
                        {error && <FormError message={error} />}
                        <FormField htmlFor='logpush-name' label='Name' required>
                            <Input
                                id='logpush-name'
                                maxLength={120}
                                value={name}
                                variant='secondary'
                                onChange={(event) => setName(event.target.value)}
                            />
                        </FormField>
                        <FormField
                            hint='Comma-separated host:port list, e.g. kafka-1:9092, kafka-2:9092.'
                            htmlFor='logpush-brokers'
                            label='Kafka brokers'
                            required={!editing}
                        >
                            <Input
                                autoCapitalize='none'
                                autoComplete='off'
                                id='logpush-brokers'
                                placeholder='kafka-1:9092, kafka-2:9092'
                                value={brokers}
                                variant='secondary'
                                onChange={(event) => {
                                    setBrokers(event.target.value);
                                    setTestResult(null);
                                }}
                            />
                        </FormField>
                        <FormField htmlFor='logpush-topic' label='Topic' required={!editing}>
                            <Input
                                autoCapitalize='none'
                                autoComplete='off'
                                id='logpush-topic'
                                placeholder='goveto-access-logs'
                                value={topic}
                                variant='secondary'
                                onChange={(event) => {
                                    setTopic(event.target.value);
                                    setTestResult(null);
                                }}
                            />
                        </FormField>
                        <FormField label='Log types' required>
                            <div className='grid grid-cols-1 gap-2 sm:grid-cols-2'>
                                {logTypeOptions.map((option) => (
                                    <div
                                        className='flex items-center justify-between gap-3 rounded-lg border border-border px-3 py-2'
                                        key={option.id}
                                    >
                                        <span className='text-sm'>{option.label}</span>
                                        <ToggleSwitch
                                            isSelected={logTypes.includes(option.id)}
                                            label={option.label}
                                            onChange={(selected) =>
                                                toggleLogType(option.id, selected)
                                            }
                                        />
                                    </div>
                                ))}
                            </div>
                        </FormField>
                        <SelectField
                            label='SASL mechanism'
                            options={saslOptions}
                            value={saslMechanism}
                            variant='secondary'
                            onChange={(value) => {
                                setSaslMechanism(value);
                                if (value === 'none') {
                                    setUsername('');
                                    setPassword('');
                                }
                                setTestResult(null);
                            }}
                        />
                        {saslMechanism !== 'none' && (
                            <>
                                <FormField
                                    htmlFor='logpush-username'
                                    label='Username'
                                    required={!editing}
                                >
                                    <Input
                                        autoCapitalize='none'
                                        autoComplete='off'
                                        id='logpush-username'
                                        spellCheck={false}
                                        value={username}
                                        variant='secondary'
                                        onChange={(event) => {
                                            setUsername(event.target.value);
                                            setTestResult(null);
                                        }}
                                    />
                                </FormField>
                                <FormField
                                    hint={
                                        editing
                                            ? 'Leave empty to keep the stored credentials.'
                                            : undefined
                                    }
                                    htmlFor='logpush-password'
                                    label='Password'
                                    required={!editing}
                                >
                                    <Input
                                        autoComplete='off'
                                        id='logpush-password'
                                        type='password'
                                        value={password}
                                        variant='secondary'
                                        onChange={(event) => {
                                            setPassword(event.target.value);
                                            setTestResult(null);
                                        }}
                                    />
                                </FormField>
                            </>
                        )}
                        <div className='flex items-center justify-between gap-3'>
                            <span className='text-sm font-medium'>TLS</span>
                            <ToggleSwitch
                                isSelected={tlsEnabled}
                                label='TLS'
                                onChange={(selected) => {
                                    setTlsEnabled(selected);
                                    setTestResult(null);
                                }}
                            />
                        </div>
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
                            isDisabled={testing || Boolean(busyID)}
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
                confirmLabel='Delete destination'
                description={
                    deleteTarget ? `Delete ${deleteTarget.name} from this cluster?` : undefined
                }
                isOpen={Boolean(deleteTarget)}
                loading={Boolean(busyID)}
                title='Delete logpush destination'
                onConfirm={() => void remove()}
                onOpenChange={(open) => {
                    if (!open) setDeleteTarget(null);
                }}
            />
        </div>
    );
}
