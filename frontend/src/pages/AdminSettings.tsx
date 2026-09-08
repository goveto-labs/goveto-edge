import type {
    AdminSettings as AdminSettingsData,
    AuthenticationProviderSettings,
    UpdateAdminSettings,
} from '@/api';

import { Alert, Button, Input, Spinner, Tooltip } from '@heroui/react';
import {
    AlertTriangle,
    FileClock,
    KeyRound,
    ListTodo,
    Network,
    Pencil,
    Plus,
    Save,
    ServerCog,
    Settings2,
    ShieldCheck,
    Trash2,
    UserRoundCog,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Navigate, useNavigate, useParams } from 'react-router-dom';

import { ApiError, adminSettingsApi } from '@/api';
import {
    AdminAuthProviderDialog,
    authenticationProviderPresets,
} from '@/components/AdminAuthProviderDialog.tsx';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { ContentCard } from '@/components/ContentCard.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { ValueListAddField } from '@/components/ValueListAddField.tsx';
import { useAuth } from '@/hooks/useAuth.ts';
import { useUnsavedChanges } from '@/hooks/useUnsavedChanges.tsx';
import AuditLog from '@/pages/AuditLog.tsx';
import Users from '@/pages/Users.tsx';

type AdminTab = 'general' | 'jobs' | 'authentication' | 'providers' | 'users' | 'audit';

const tabs: Array<{
    id: AdminTab;
    label: string;
    icon: typeof Settings2;
    dividerBefore?: boolean;
}> = [
    { id: 'general', label: 'General', icon: Settings2 },
    { id: 'jobs', label: 'Jobs', icon: ListTodo },
    { id: 'authentication', label: 'Authentication', icon: ShieldCheck },
    { id: 'providers', label: 'Providers', icon: KeyRound },
    { id: 'users', label: 'Users', icon: UserRoundCog, dividerBefore: true },
    { id: 'audit', label: 'Audit log', icon: FileClock },
];

const headerPresets = [
    {
        id: 'standard',
        label: 'Standard proxy',
        headers: ['X-Forwarded-For', 'X-Real-Ip', 'Forwarded'],
    },
    {
        id: 'cloudflare',
        label: 'Cloudflare',
        headers: ['Cf-Connecting-Ip', 'True-Client-Ip', 'X-Forwarded-For'],
    },
    {
        id: 'fastly',
        label: 'Fastly',
        headers: ['Fastly-Client-Ip', 'X-Forwarded-For'],
    },
    {
        id: 'common',
        label: 'All common headers',
        headers: [
            'X-Forwarded-For',
            'X-Real-Ip',
            'Forwarded',
            'Cf-Connecting-Ip',
            'True-Client-Ip',
            'Fastly-Client-Ip',
        ],
    },
] as const;

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

function editable(settings: AdminSettingsData) {
    return {
        agent_gateway_public_address: settings.agent_gateway_public_address,
        http_proxy: settings.http_proxy,
        authentication: settings.authentication,
        job_retention: settings.job_retention,
        agent_auto_upgrade_enabled: settings.agent_auto_upgrade_enabled,
    };
}

function networkSnapshot(settings: AdminSettingsData) {
    return JSON.stringify({
        agent_gateway_public_address: settings.agent_gateway_public_address,
        http_proxy: settings.http_proxy,
    });
}

function presetFor(headers: string[]) {
    const normalized = [...headers]
        .map((header) => header.toLowerCase())
        .sort()
        .join(',');
    return (
        headerPresets.find(
            (preset) =>
                [...preset.headers]
                    .map((header) => header.toLowerCase())
                    .sort()
                    .join(',') === normalized
        )?.id ?? 'custom'
    );
}

function validHeader(value: string) {
    return /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/.test(value) ? '' : 'Enter a valid HTTP header name.';
}

function SettingToggle({
    label,
    description,
    selected,
    disabled,
    onChange,
}: {
    label: string;
    description: string;
    selected: boolean;
    disabled?: boolean;
    onChange: (selected: boolean) => void;
}) {
    return (
        <div className='flex items-start justify-between gap-5'>
            <div>
                <div className='text-sm font-medium'>{label}</div>
                <p className='mt-1 max-w-2xl text-xs leading-5 text-muted'>{description}</p>
            </div>
            <ToggleSwitch
                isDisabled={disabled}
                isSelected={selected}
                label={label}
                onChange={onChange}
            />
        </div>
    );
}

function providerPresetLabel(provider: AuthenticationProviderSettings) {
    return (
        authenticationProviderPresets.find((preset) => preset.id === provider.preset)?.label ??
        provider.type
    );
}

export default function AdminSettings() {
    const { user, loading: authLoading } = useAuth();
    const navigate = useNavigate();
    const params = useParams();
    const requestedTab = (params['*'] ?? '').split('/')[0];
    const tab: AdminTab = tabs.some((item) => item.id === requestedTab)
        ? (requestedTab as AdminTab)
        : 'general';
    const settingsTab =
        tab === 'general' || tab === 'jobs' || tab === 'authentication' || tab === 'providers';

    const [form, setForm] = useState<AdminSettingsData | null>(null);
    const [baseline, setBaseline] = useState('');
    const [baselineNetwork, setBaselineNetwork] = useState('');
    const [providerSecrets, setProviderSecrets] = useState<Record<string, string>>({});
    const [captchaSecret, setCaptchaSecret] = useState('');
    const [providerDialogOpen, setProviderDialogOpen] = useState(false);
    const [editingProvider, setEditingProvider] = useState<AuthenticationProviderSettings | null>(
        null
    );
    const [pendingDeleteProvider, setPendingDeleteProvider] =
        useState<AuthenticationProviderSettings | null>(null);
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [restarting, setRestarting] = useState(false);
    const [saved, setSaved] = useState(false);
    const [error, setError] = useState('');
    const loadedSettings = useRef<AdminSettingsData | null>(null);

    const dirty = useMemo(
        () =>
            form !== null &&
            (JSON.stringify(editable(form)) !== baseline ||
                Object.values(providerSecrets).some(Boolean) ||
                captchaSecret.trim() !== ''),
        [baseline, captchaSecret, form, providerSecrets]
    );
    const restartAffected = form !== null && networkSnapshot(form) !== baselineNetwork;
    const retentionValid =
        form !== null &&
        form.job_retention.history_days >= 7 &&
        form.job_retention.history_days <= 3650 &&
        form.job_retention.versions_per_site >= 2 &&
        form.job_retention.versions_per_site <= 1000;
    const enabledProviderCount =
        form?.authentication.providers.filter((provider) => provider.enabled).length ?? 0;

    const applySettings = useCallback((settings: AdminSettingsData) => {
        loadedSettings.current = settings;
        setForm(settings);
        setBaseline(JSON.stringify(editable(settings)));
        setBaselineNetwork(networkSnapshot(settings));
        setProviderSecrets({});
        setCaptchaSecret('');
    }, []);
    const { requestAction } = useUnsavedChanges(dirty, 'admin-settings', () => {
        if (loadedSettings.current) applySettings(loadedSettings.current);
    });

    useEffect(() => {
        if (user?.role !== 'ADMIN' || !settingsTab || form) return;
        let active = true;
        setLoading(true);
        adminSettingsApi
            .get()
            .then((settings) => {
                if (!active) return;
                applySettings(settings);
                setError('');
            })
            .catch((loadError) => {
                if (active) setError(errorMessage(loadError, 'Failed to load admin settings'));
            })
            .finally(() => {
                if (active) setLoading(false);
            });
        return () => {
            active = false;
        };
    }, [applySettings, form, settingsTab, user?.role]);

    if (!authLoading && user?.role !== 'ADMIN') {
        return <Navigate replace to='/' />;
    }

    const updateForm = (next: AdminSettingsData) => {
        setForm(next);
        setRestarting(false);
        setSaved(false);
    };

    const saveSettings = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!form || !dirty) return;
        if (!form.authentication.local_login_enabled && enabledProviderCount === 0) {
            setError('Local login or an external provider must remain enabled.');
            return;
        }
        if (
            form.authentication.require_totp &&
            form.authentication.providers.some(
                (provider) => provider.enabled && provider.auto_create_users
            )
        ) {
            setError('Automatic external user creation cannot be combined with required TOTP.');
            return;
        }
        const registration = form.authentication.registration;
        if (registration.enabled && !form.authentication.local_login_enabled) {
            setError('Public registration requires local login.');
            return;
        }
        if (
            registration.enabled &&
            (!registration.captcha_provider ||
                !registration.captcha_site_key.trim() ||
                (!registration.captcha_secret_configured && !captchaSecret.trim()))
        ) {
            setError('Public registration requires a CAPTCHA provider, site key, and secret key.');
            return;
        }
        setSaving(true);
        setError('');
        try {
            const payload: UpdateAdminSettings = {
                ...editable(form),
                authentication: {
                    ...form.authentication,
                    registration: {
                        ...form.authentication.registration,
                        captcha_secret: captchaSecret.trim() || undefined,
                    },
                    providers: form.authentication.providers.map((provider) => ({
                        ...provider,
                        client_secret: providerSecrets[provider.id] || undefined,
                    })),
                },
                restart: true,
            };
            const settings = await adminSettingsApi.update(payload);
            applySettings(settings);
            setRestarting(settings.restarting);
            setSaved(!settings.restarting);
        } catch (saveError) {
            setError(errorMessage(saveError, 'Failed to save admin settings'));
        } finally {
            setSaving(false);
        }
    };

    const openProviderDialog = (provider: AuthenticationProviderSettings | null) => {
        setEditingProvider(provider);
        setProviderDialogOpen(true);
    };

    const applyProvider = (provider: AuthenticationProviderSettings, clientSecret: string) => {
        if (!form) return;
        const exists = form.authentication.providers.some((item) => item.id === provider.id);
        const providers = exists
            ? form.authentication.providers.map((item) =>
                  item.id === provider.id ? provider : item
              )
            : [...form.authentication.providers, provider];
        updateForm({
            ...form,
            authentication: { ...form.authentication, providers },
        });
        if (clientSecret) {
            setProviderSecrets((current) => ({ ...current, [provider.id]: clientSecret }));
        }
        setProviderDialogOpen(false);
        setEditingProvider(null);
    };

    const deleteProvider = (provider: AuthenticationProviderSettings) => {
        if (!form) return;
        updateForm({
            ...form,
            authentication: {
                ...form.authentication,
                providers: form.authentication.providers.filter((item) => item.id !== provider.id),
            },
        });
        setProviderSecrets((current) => {
            const next = { ...current };
            delete next[provider.id];
            return next;
        });
    };

    return (
        <div className='mx-auto max-w-[1400px] space-y-5'>
            <PageHeader
                subtitle='Instance-wide configuration, access, and audit controls.'
                title='Admin settings'
            />

            <div className='grid gap-6 lg:grid-cols-[200px_minmax(0,1fr)]'>
                <nav
                    aria-label='Admin settings sections'
                    className='flex flex-col gap-1 lg:sticky lg:top-0 lg:self-start'
                >
                    {tabs.map((item) => {
                        const Icon = item.icon;
                        return (
                            <div key={item.id}>
                                {item.dividerBefore && (
                                    <div className='my-2 border-t border-border' />
                                )}
                                <button
                                    aria-current={tab === item.id ? 'page' : undefined}
                                    className={`flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium transition-colors ${tab === item.id ? 'bg-surface-secondary text-foreground' : 'text-muted hover:bg-surface-secondary hover:text-foreground'}`}
                                    type='button'
                                    onClick={() => {
                                        const go = () => navigate(`/settings/admin/${item.id}`);
                                        if (
                                            settingsTab &&
                                            item.id !== 'users' &&
                                            item.id !== 'audit'
                                        )
                                            go();
                                        else requestAction(go);
                                    }}
                                >
                                    <Icon className='h-4 w-4 shrink-0' />
                                    {item.label}
                                </button>
                            </div>
                        );
                    })}
                </nav>

                <div className='min-w-0 space-y-5'>
                    {tab === 'users' && <Users embedded />}
                    {tab === 'audit' && <AuditLog embedded />}

                    {settingsTab && error && <FormError message={error} />}
                    {settingsTab && restarting && (
                        <Alert status='success'>
                            <Alert.Indicator />
                            <Alert.Content>
                                <Alert.Title>Restart requested</Alert.Title>
                                <Alert.Description>
                                    The control plane is shutting down gracefully. Its service
                                    supervisor must start it again before this page can reconnect.
                                </Alert.Description>
                            </Alert.Content>
                        </Alert>
                    )}
                    {settingsTab && saved && (
                        <Alert status='success'>
                            <Alert.Indicator />
                            <Alert.Content>
                                <Alert.Title>Settings saved</Alert.Title>
                                <Alert.Description>The changes are active.</Alert.Description>
                            </Alert.Content>
                        </Alert>
                    )}

                    {settingsTab &&
                        (loading || authLoading || !form ? (
                            <ContentCard>
                                <div className='flex min-h-32 items-center justify-center'>
                                    <Spinner />
                                </div>
                            </ContentCard>
                        ) : (
                            <form className='space-y-5' onSubmit={saveSettings}>
                                {tab === 'general' && (
                                    <div className='space-y-5'>
                                        <ContentCard
                                            title={
                                                <span className='flex items-center gap-2'>
                                                    <ServerCog className='h-4 w-4 text-muted' />
                                                    Connectivity
                                                </span>
                                            }
                                        >
                                            <FormField
                                                htmlFor='agent-gateway-public-address'
                                                label='Agent gateway public address'
                                                hint='Use host:port. IPv6 addresses require brackets.'
                                                required
                                            >
                                                <Input
                                                    autoCapitalize='none'
                                                    autoComplete='off'
                                                    id='agent-gateway-public-address'
                                                    placeholder='edge.example.com:8443'
                                                    required
                                                    spellCheck={false}
                                                    value={form.agent_gateway_public_address}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        updateForm({
                                                            ...form,
                                                            agent_gateway_public_address:
                                                                event.target.value,
                                                        })
                                                    }
                                                />
                                            </FormField>
                                        </ContentCard>
                                        <ContentCard
                                            title={
                                                <span className='flex items-center gap-2'>
                                                    <Network className='h-4 w-4 text-muted' />
                                                    Client IP resolution
                                                </span>
                                            }
                                        >
                                            <div className='gap-3 grid'>
                                                <SettingToggle
                                                    description='Accept the configured client IP headers from every direct connection.'
                                                    label='Trust headers from all sources'
                                                    selected={form.http_proxy.trust_all}
                                                    onChange={(trustAll) =>
                                                        updateForm({
                                                            ...form,
                                                            http_proxy: {
                                                                ...form.http_proxy,
                                                                trust_all: trustAll,
                                                            },
                                                        })
                                                    }
                                                />
                                                {form.http_proxy.trust_all && (
                                                    <div className='flex items-center gap-2 py-3 text-xs text-warning'>
                                                        <AlertTriangle className='mt-0.5 h-4 w-4 shrink-0 text-warning' />
                                                        Direct clients can spoof these headers
                                                        unless an external proxy strips them.
                                                    </div>
                                                )}
                                                <SelectField
                                                    label='Header preset'
                                                    options={[
                                                        ...headerPresets.map((preset) => ({
                                                            id: preset.id,
                                                            label: preset.label,
                                                        })),
                                                        { id: 'custom', label: 'Custom' },
                                                    ]}
                                                    value={presetFor(
                                                        form.http_proxy.client_ip_headers
                                                    )}
                                                    variant='secondary'
                                                    onChange={(value) => {
                                                        const preset = headerPresets.find(
                                                            (item) => item.id === value
                                                        );
                                                        if (!preset) return;
                                                        updateForm({
                                                            ...form,
                                                            http_proxy: {
                                                                ...form.http_proxy,
                                                                client_ip_headers: [
                                                                    ...preset.headers,
                                                                ],
                                                            },
                                                        });
                                                    }}
                                                />
                                                <ValueListAddField
                                                    addLabel='Add header'
                                                    dialogTitle='Add client IP header'
                                                    emptyLabel='No headers selected'
                                                    label='Allowed client IP headers'
                                                    placeholder='X-Forwarded-For'
                                                    validate={validHeader}
                                                    values={form.http_proxy.client_ip_headers}
                                                    onChange={(headers) =>
                                                        updateForm({
                                                            ...form,
                                                            http_proxy: {
                                                                ...form.http_proxy,
                                                                client_ip_headers: headers,
                                                            },
                                                        })
                                                    }
                                                />
                                            </div>
                                        </ContentCard>
                                    </div>
                                )}

                                {tab === 'authentication' && (
                                    <div className='space-y-5'>
                                        <ContentCard
                                            title={
                                                <span className='flex items-center gap-2'>
                                                    <ShieldCheck className='h-4 w-4 text-muted' />
                                                    Sign-in policy
                                                </span>
                                            }
                                        >
                                            <div className='grid gap-3'>
                                                <SettingToggle
                                                    description='Allow email and password sign-in. Keep this enabled while setting up or changing providers.'
                                                    label='Local login'
                                                    selected={
                                                        form.authentication.local_login_enabled
                                                    }
                                                    onChange={(localLoginEnabled) =>
                                                        updateForm({
                                                            ...form,
                                                            authentication: {
                                                                ...form.authentication,
                                                                local_login_enabled:
                                                                    localLoginEnabled,
                                                            },
                                                        })
                                                    }
                                                />
                                                <SettingToggle
                                                    description='Require active users to enroll a time-based one-time password.'
                                                    label='Require TOTP'
                                                    selected={form.authentication.require_totp}
                                                    onChange={(requireTOTP) =>
                                                        updateForm({
                                                            ...form,
                                                            authentication: {
                                                                ...form.authentication,
                                                                require_totp: requireTOTP,
                                                                providers: requireTOTP
                                                                    ? form.authentication.providers.map(
                                                                          (provider) => ({
                                                                              ...provider,
                                                                              auto_create_users: false,
                                                                          })
                                                                      )
                                                                    : form.authentication.providers,
                                                            },
                                                        })
                                                    }
                                                />
                                            </div>
                                        </ContentCard>

                                        <ContentCard
                                            title={
                                                <span className='flex items-center gap-2'>
                                                    <UserRoundCog className='h-4 w-4 text-muted' />
                                                    Public registration
                                                </span>
                                            }
                                        >
                                            <div className='space-y-5'>
                                                <SettingToggle
                                                    description='Allow new users to create viewer accounts after completing a CAPTCHA challenge.'
                                                    label='Public registration'
                                                    selected={
                                                        form.authentication.registration.enabled
                                                    }
                                                    onChange={(enabled) =>
                                                        updateForm({
                                                            ...form,
                                                            authentication: {
                                                                ...form.authentication,
                                                                registration: {
                                                                    ...form.authentication
                                                                        .registration,
                                                                    enabled,
                                                                },
                                                            },
                                                        })
                                                    }
                                                />
                                                <div className='grid gap-4 border-t border-border pt-5 sm:grid-cols-2'>
                                                    <SelectField
                                                        id='captcha-provider'
                                                        label='CAPTCHA provider'
                                                        options={[
                                                            {
                                                                id: 'cloudflare',
                                                                label: 'Cloudflare Turnstile',
                                                            },
                                                            {
                                                                id: 'recaptcha',
                                                                label: 'Google reCAPTCHA',
                                                            },
                                                        ]}
                                                        placeholder='Select a provider'
                                                        value={
                                                            form.authentication.registration
                                                                .captcha_provider
                                                        }
                                                        variant='secondary'
                                                        onChange={(captchaProvider) =>
                                                            updateForm({
                                                                ...form,
                                                                authentication: {
                                                                    ...form.authentication,
                                                                    registration: {
                                                                        ...form.authentication
                                                                            .registration,
                                                                        captcha_provider:
                                                                            captchaProvider as
                                                                                | 'cloudflare'
                                                                                | 'recaptcha',
                                                                        captcha_secret_configured:
                                                                            captchaProvider ===
                                                                            form.authentication
                                                                                .registration
                                                                                .captcha_provider
                                                                                ? form
                                                                                      .authentication
                                                                                      .registration
                                                                                      .captcha_secret_configured
                                                                                : false,
                                                                    },
                                                                },
                                                            })
                                                        }
                                                    />
                                                    <FormField
                                                        htmlFor='captcha-site-key'
                                                        label='Site key'
                                                        required={
                                                            form.authentication.registration.enabled
                                                        }
                                                    >
                                                        <Input
                                                            autoComplete='off'
                                                            id='captcha-site-key'
                                                            required={
                                                                form.authentication.registration
                                                                    .enabled
                                                            }
                                                            value={
                                                                form.authentication.registration
                                                                    .captcha_site_key
                                                            }
                                                            variant='secondary'
                                                            onChange={(event) =>
                                                                updateForm({
                                                                    ...form,
                                                                    authentication: {
                                                                        ...form.authentication,
                                                                        registration: {
                                                                            ...form.authentication
                                                                                .registration,
                                                                            captcha_site_key:
                                                                                event.target.value,
                                                                        },
                                                                    },
                                                                })
                                                            }
                                                        />
                                                    </FormField>
                                                    <FormField
                                                        className='sm:col-span-2'
                                                        htmlFor='captcha-secret'
                                                        label='Secret key'
                                                        hint={
                                                            form.authentication.registration
                                                                .captcha_secret_configured
                                                                ? 'A secret is configured. Leave blank to keep it, or enter a replacement.'
                                                                : 'Stored encrypted and never returned by the API.'
                                                        }
                                                        required={
                                                            form.authentication.registration
                                                                .enabled &&
                                                            !form.authentication.registration
                                                                .captcha_secret_configured
                                                        }
                                                    >
                                                        <Input
                                                            autoComplete='new-password'
                                                            id='captcha-secret'
                                                            placeholder={
                                                                form.authentication.registration
                                                                    .captcha_secret_configured
                                                                    ? 'Configured'
                                                                    : undefined
                                                            }
                                                            required={
                                                                form.authentication.registration
                                                                    .enabled &&
                                                                !form.authentication.registration
                                                                    .captcha_secret_configured
                                                            }
                                                            type='password'
                                                            value={captchaSecret}
                                                            variant='secondary'
                                                            onChange={(event) => {
                                                                setCaptchaSecret(
                                                                    event.target.value
                                                                );
                                                                setSaved(false);
                                                            }}
                                                        />
                                                    </FormField>
                                                </div>
                                            </div>
                                        </ContentCard>

                                        <ContentCard
                                            action={
                                                <Button
                                                    size='sm'
                                                    type='button'
                                                    variant='secondary'
                                                    onPress={() =>
                                                        navigate('/settings/admin/providers')
                                                    }
                                                >
                                                    Manage providers
                                                </Button>
                                            }
                                            title='External sign-in'
                                        >
                                            <div className='grid gap-4 sm:grid-cols-3'>
                                                <div>
                                                    <div className='text-xs text-muted'>
                                                        Configured
                                                    </div>
                                                    <div className='mt-1 text-xl font-semibold'>
                                                        {form.authentication.providers.length}
                                                    </div>
                                                </div>
                                                <div>
                                                    <div className='text-xs text-muted'>
                                                        Enabled
                                                    </div>
                                                    <div className='mt-1 text-xl font-semibold'>
                                                        {enabledProviderCount}
                                                    </div>
                                                </div>
                                                <div>
                                                    <div className='text-xs text-muted'>
                                                        Automatic users
                                                    </div>
                                                    <div className='mt-1 text-xl font-semibold'>
                                                        {
                                                            form.authentication.providers.filter(
                                                                (provider) =>
                                                                    provider.auto_create_users
                                                            ).length
                                                        }
                                                    </div>
                                                </div>
                                            </div>
                                        </ContentCard>
                                    </div>
                                )}

                                {tab === 'jobs' && (
                                    <div className='space-y-5'>
                                        <ContentCard
                                            title={
                                                <span className='flex items-center gap-2'>
                                                    <ServerCog className='h-4 w-4 text-muted' />
                                                    Agent updates
                                                </span>
                                            }
                                        >
                                            <SettingToggle
                                                description='Upgrade online agents to the version embedded in this control plane. Disabling cancels queued automatic upgrades; an upgrade already replacing a binary will finish verification or roll back.'
                                                label='Automatic agent upgrades'
                                                selected={form.agent_auto_upgrade_enabled}
                                                onChange={(enabled) =>
                                                    updateForm({
                                                        ...form,
                                                        agent_auto_upgrade_enabled: enabled,
                                                    })
                                                }
                                            />
                                        </ContentCard>
                                        <ContentCard
                                            title={
                                                <span className='flex items-center gap-2'>
                                                    <ListTodo className='h-4 w-4 text-muted' />
                                                    History retention
                                                </span>
                                            }
                                        >
                                            <div className='grid gap-5 sm:grid-cols-2'>
                                                <FormField
                                                    htmlFor='job-history-days'
                                                    label='Job history (days)'
                                                    hint={
                                                        "Terminal jobs and their execution attempts older than this window are removed. Active jobs and each site's latest successful publish remain protected."
                                                    }
                                                >
                                                    <Input
                                                        id='job-history-days'
                                                        max={3650}
                                                        min={7}
                                                        required
                                                        type='number'
                                                        value={String(
                                                            form.job_retention.history_days
                                                        )}
                                                        variant='secondary'
                                                        onChange={(event) =>
                                                            updateForm({
                                                                ...form,
                                                                job_retention: {
                                                                    ...form.job_retention,
                                                                    history_days: Number(
                                                                        event.target.value
                                                                    ),
                                                                },
                                                            })
                                                        }
                                                    />
                                                </FormField>
                                                <FormField
                                                    htmlFor='config-versions-per-site'
                                                    label='Minimum versions per site'
                                                    hint='Keep at least this many recent configuration versions per site, even after the history window expires. Current and rollback baseline versions are always protected.'
                                                >
                                                    <Input
                                                        id='config-versions-per-site'
                                                        max={1000}
                                                        min={2}
                                                        required
                                                        type='number'
                                                        value={String(
                                                            form.job_retention.versions_per_site
                                                        )}
                                                        variant='secondary'
                                                        onChange={(event) =>
                                                            updateForm({
                                                                ...form,
                                                                job_retention: {
                                                                    ...form.job_retention,
                                                                    versions_per_site: Number(
                                                                        event.target.value
                                                                    ),
                                                                },
                                                            })
                                                        }
                                                    />
                                                </FormField>
                                            </div>
                                            <p className='mt-5 border-t border-border pt-4 text-xs leading-5 text-muted'>
                                                Cleanup runs hourly in batches. Changes apply
                                                without a control-plane restart.
                                            </p>
                                        </ContentCard>
                                    </div>
                                )}

                                {tab === 'providers' && (
                                    <ContentCard
                                        action={
                                            <Button
                                                size='sm'
                                                type='button'
                                                variant='primary'
                                                onPress={() => openProviderDialog(null)}
                                            >
                                                <Plus className='h-4 w-4' />
                                                Add provider
                                            </Button>
                                        }
                                        noPadding
                                        title={
                                            <span className='flex items-center gap-2'>
                                                <KeyRound className='h-4 w-4 text-muted' />
                                                OAuth 2.0 and OpenID Connect providers
                                            </span>
                                        }
                                    >
                                        {form.authentication.providers.length === 0 ? (
                                            <div className='flex min-h-48 flex-col items-center justify-center px-6 py-10 text-center'>
                                                <KeyRound className='h-8 w-8 text-muted' />
                                                <div className='mt-3 text-sm font-medium'>
                                                    No providers configured
                                                </div>
                                                <p className='mt-1 max-w-sm text-xs leading-5 text-muted'>
                                                    Add a provider to offer single sign-on on the
                                                    login page.
                                                </p>
                                            </div>
                                        ) : (
                                            <div className='divide-y divide-border'>
                                                {form.authentication.providers.map((provider) => (
                                                    <div
                                                        className='flex flex-col gap-3 px-4 py-4 sm:flex-row sm:items-center sm:justify-between'
                                                        key={provider.id}
                                                    >
                                                        <div className='min-w-0'>
                                                            <div className='flex flex-wrap items-center gap-2'>
                                                                <span className='truncate text-sm font-semibold'>
                                                                    {provider.provider_name}
                                                                </span>
                                                                <span
                                                                    className={`rounded-md border px-1.5 py-0.5 text-[11px] font-medium ${provider.enabled ? 'border-success/30 bg-success/10 text-success' : 'border-border bg-surface-secondary text-muted'}`}
                                                                >
                                                                    {provider.enabled
                                                                        ? 'Enabled'
                                                                        : 'Disabled'}
                                                                </span>
                                                            </div>
                                                            <p className='mt-1 truncate text-xs text-muted'>
                                                                {providerPresetLabel(provider)},{' '}
                                                                {provider.type}.
                                                                {provider.client_id
                                                                    ? ` Client ${provider.client_id}`
                                                                    : ' Client ID not set'}
                                                            </p>
                                                        </div>
                                                        <div className='flex shrink-0 items-center gap-2'>
                                                            <ToggleSwitch
                                                                isSelected={provider.enabled}
                                                                label={`Enable ${provider.provider_name}`}
                                                                onChange={(enabled) =>
                                                                    updateForm({
                                                                        ...form,
                                                                        authentication: {
                                                                            ...form.authentication,
                                                                            providers:
                                                                                form.authentication.providers.map(
                                                                                    (item) =>
                                                                                        item.id ===
                                                                                        provider.id
                                                                                            ? {
                                                                                                  ...item,
                                                                                                  enabled,
                                                                                              }
                                                                                            : item
                                                                                ),
                                                                        },
                                                                    })
                                                                }
                                                            />
                                                            <Tooltip>
                                                                <Tooltip.Trigger>
                                                                    <Button
                                                                        isIconOnly
                                                                        aria-label={`Edit ${provider.provider_name}`}
                                                                        size='sm'
                                                                        type='button'
                                                                        variant='ghost'
                                                                        onPress={() =>
                                                                            openProviderDialog(
                                                                                provider
                                                                            )
                                                                        }
                                                                    >
                                                                        <Pencil className='h-4 w-4' />
                                                                    </Button>
                                                                </Tooltip.Trigger>
                                                                <Tooltip.Content>
                                                                    Edit provider
                                                                </Tooltip.Content>
                                                            </Tooltip>
                                                            <Tooltip>
                                                                <Tooltip.Trigger>
                                                                    <Button
                                                                        isIconOnly
                                                                        aria-label={`Delete ${provider.provider_name}`}
                                                                        className='text-danger'
                                                                        size='sm'
                                                                        type='button'
                                                                        variant='ghost'
                                                                        onPress={() =>
                                                                            setPendingDeleteProvider(
                                                                                provider
                                                                            )
                                                                        }
                                                                    >
                                                                        <Trash2 className='h-4 w-4' />
                                                                    </Button>
                                                                </Tooltip.Trigger>
                                                                <Tooltip.Content>
                                                                    Delete provider
                                                                </Tooltip.Content>
                                                            </Tooltip>
                                                        </div>
                                                    </div>
                                                ))}
                                            </div>
                                        )}
                                    </ContentCard>
                                )}

                                <div className='sticky bottom-4 flex flex-col gap-3 rounded-xl border border-border bg-surface px-4 py-3 shadow-lg sm:flex-row sm:items-center sm:justify-between'>
                                    <div className='flex min-w-0 items-center gap-2 text-xs text-muted'>
                                        {restartAffected && (
                                            <AlertTriangle className='mt-0.5 h-4 w-4 shrink-0 text-warning' />
                                        )}
                                        {restartAffected
                                            ? 'Connectivity and client IP changes restart the control plane after saving.'
                                            : dirty
                                              ? 'These changes take effect only after you save them.'
                                              : 'Changes on this page remain a draft until you save them.'}
                                    </div>
                                    <Button
                                        className='shrink-0'
                                        isDisabled={
                                            saving ||
                                            !dirty ||
                                            !retentionValid ||
                                            !form.agent_gateway_public_address.trim()
                                        }
                                        type='submit'
                                        variant='primary'
                                    >
                                        <Save className='h-4 w-4' />
                                        {saving ? 'Saving...' : 'Save changes'}
                                    </Button>
                                </div>
                            </form>
                        ))}
                </div>
            </div>

            {settingsTab && form && (
                <AdminAuthProviderDialog
                    clientSecret={
                        editingProvider ? (providerSecrets[editingProvider.id] ?? '') : ''
                    }
                    defaultRedirectURL={`${window.location.origin}/api/v1/auth/providers/callback`}
                    isOpen={providerDialogOpen}
                    provider={editingProvider}
                    requireTOTP={form.authentication.require_totp}
                    onClose={() => {
                        setProviderDialogOpen(false);
                        setEditingProvider(null);
                    }}
                    onSave={applyProvider}
                />
            )}

            <ConfirmDialog
                confirmLabel='Delete'
                danger
                description={
                    pendingDeleteProvider
                        ? `Delete the ${pendingDeleteProvider.provider_name} provider?`
                        : undefined
                }
                isOpen={pendingDeleteProvider !== null}
                title='Delete provider?'
                onConfirm={() => {
                    const provider = pendingDeleteProvider;
                    setPendingDeleteProvider(null);
                    if (provider) deleteProvider(provider);
                }}
                onOpenChange={(open) => {
                    if (!open) setPendingDeleteProvider(null);
                }}
            />
        </div>
    );
}
