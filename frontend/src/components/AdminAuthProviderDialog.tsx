import type { AuthenticationProviderSettings, AuthenticationProviderType } from '@/api';

import { Alert, Button, Input } from '@heroui/react';
import { KeyRound } from 'lucide-react';
import { useEffect, useState } from 'react';

import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

interface ProviderPreset {
    id: string;
    label: string;
    type: AuthenticationProviderType;
    providerName: string;
    issuerURL?: string;
    authorizationURL?: string;
    tokenURL?: string;
    userInfoURL?: string;
    emailURL?: string;
    scopes: string[];
    issuerPlaceholder?: string;
}

export const authenticationProviderPresets: ProviderPreset[] = [
    {
        id: 'GOOGLE',
        label: 'Google',
        type: 'OIDC',
        providerName: 'Google',
        issuerURL: 'https://accounts.google.com',
        scopes: ['openid', 'email', 'profile'],
    },
    {
        id: 'MICROSOFT_ENTRA',
        label: 'Microsoft Entra ID',
        type: 'OIDC',
        providerName: 'Microsoft Entra ID',
        scopes: ['openid', 'email', 'profile'],
        issuerPlaceholder: 'https://login.microsoftonline.com/<tenant-id>/v2.0',
    },
    {
        id: 'GITHUB',
        label: 'GitHub',
        type: 'OAUTH2',
        providerName: 'GitHub',
        authorizationURL: 'https://github.com/login/oauth/authorize',
        tokenURL: 'https://github.com/login/oauth/access_token',
        userInfoURL: 'https://api.github.com/user',
        emailURL: 'https://api.github.com/user/emails',
        scopes: ['read:user', 'user:email'],
    },
    {
        id: 'OKTA',
        label: 'Okta',
        type: 'OIDC',
        providerName: 'Okta',
        scopes: ['openid', 'email', 'profile'],
        issuerPlaceholder: 'https://company.okta.com/oauth2/default',
    },
    {
        id: 'AUTH0',
        label: 'Auth0',
        type: 'OIDC',
        providerName: 'Auth0',
        scopes: ['openid', 'email', 'profile'],
        issuerPlaceholder: 'https://tenant.us.auth0.com',
    },
    {
        id: 'KEYCLOAK',
        label: 'Keycloak',
        type: 'OIDC',
        providerName: 'Keycloak',
        scopes: ['openid', 'email', 'profile'],
        issuerPlaceholder: 'https://id.example.com/realms/company',
    },
    {
        id: 'CUSTOM_OIDC',
        label: 'Custom OpenID Connect',
        type: 'OIDC',
        providerName: 'Single sign-on',
        scopes: ['openid', 'email', 'profile'],
        issuerPlaceholder: 'https://id.example.com',
    },
    {
        id: 'GENERIC_OAUTH2',
        label: 'Generic OAuth 2.0',
        type: 'OAUTH2',
        providerName: 'OAuth 2.0',
        scopes: ['email', 'profile'],
    },
];

function providerFromPreset(
    preset: ProviderPreset,
    redirectURL: string
): AuthenticationProviderSettings {
    return {
        id: crypto.randomUUID(),
        type: preset.type,
        preset: preset.id,
        enabled: false,
        provider_name: preset.providerName,
        issuer_url: preset.issuerURL ?? '',
        authorization_url: preset.authorizationURL ?? '',
        token_url: preset.tokenURL ?? '',
        user_info_url: preset.userInfoURL ?? '',
        email_url: preset.emailURL ?? '',
        client_id: '',
        client_secret_configured: false,
        redirect_url: redirectURL,
        scopes: [...preset.scopes],
        auto_create_users: false,
    };
}

export function AdminAuthProviderDialog({
    isOpen,
    provider,
    clientSecret,
    defaultRedirectURL,
    requireTOTP,
    onClose,
    onSave,
}: {
    isOpen: boolean;
    provider: AuthenticationProviderSettings | null;
    clientSecret: string;
    defaultRedirectURL: string;
    requireTOTP: boolean;
    onClose: () => void;
    onSave: (provider: AuthenticationProviderSettings, clientSecret: string) => void;
}) {
    const [draft, setDraft] = useState<AuthenticationProviderSettings>(() =>
        providerFromPreset(authenticationProviderPresets[0], defaultRedirectURL)
    );
    const [secret, setSecret] = useState('');
    const [error, setError] = useState('');
    const editing = provider !== null;
    const preset =
        authenticationProviderPresets.find((item) => item.id === draft.preset) ??
        authenticationProviderPresets[authenticationProviderPresets.length - 2];

    useEffect(() => {
        if (!isOpen) return;
        setDraft(
            provider ?? providerFromPreset(authenticationProviderPresets[0], defaultRedirectURL)
        );
        setSecret(clientSecret);
        setError('');
    }, [clientSecret, defaultRedirectURL, isOpen, provider]);

    const changePreset = (presetID: string) => {
        const selected = authenticationProviderPresets.find((item) => item.id === presetID);
        if (!selected) return;
        const defaults = providerFromPreset(selected, draft.redirect_url || defaultRedirectURL);
        setDraft({
            ...defaults,
            id: draft.id,
            enabled: draft.enabled,
            client_id: draft.client_id,
            client_secret_configured: draft.client_secret_configured,
            auto_create_users: draft.auto_create_users,
        });
        setError('');
    };

    const submit = () => {
        const name = draft.provider_name.trim();
        if (!name) {
            setError('Provider name is required.');
            return;
        }
        if (draft.enabled) {
            if (!draft.client_id.trim() || (!draft.client_secret_configured && !secret.trim())) {
                setError('Enabled providers require a client ID and client secret.');
                return;
            }
            if (!draft.redirect_url.trim()) {
                setError('Redirect URL is required.');
                return;
            }
            if (draft.type === 'OIDC' && !draft.issuer_url.trim()) {
                setError('OIDC providers require an issuer URL.');
                return;
            }
            if (
                draft.type === 'OAUTH2' &&
                (!draft.authorization_url.trim() ||
                    !draft.token_url.trim() ||
                    !draft.user_info_url.trim())
            ) {
                setError('OAuth 2.0 providers require authorization, token, and user info URLs.');
                return;
            }
        }
        if (requireTOTP && draft.auto_create_users) {
            setError('Automatic user creation is unavailable while TOTP enrollment is required.');
            return;
        }
        onSave({ ...draft, provider_name: name }, secret);
    };

    return (
        <DialogShell
            icon={<KeyRound className='h-5 w-5' />}
            isOpen={isOpen}
            size='lg'
            subtitle='Configure authorization and account matching for this identity provider.'
            title={editing ? 'Edit provider' : 'Add provider'}
            onOpenChange={(open) => {
                if (!open) onClose();
            }}
        >
            <div className='max-h-[70dvh] space-y-5 overflow-y-auto px-6 py-5'>
                {error && <FormError message={error} />}

                <div className='grid gap-4 sm:grid-cols-2'>
                    <SelectField
                        isRequired
                        label='Provider preset'
                        options={authenticationProviderPresets.map((item) => ({
                            id: item.id,
                            label: item.label,
                        }))}
                        value={draft.preset}
                        variant='secondary'
                        onChange={changePreset}
                    />
                    <FormField htmlFor='auth-provider-name' label='Display name' required>
                        <Input
                            id='auth-provider-name'
                            value={draft.provider_name}
                            variant='secondary'
                            onChange={(event) =>
                                setDraft({ ...draft, provider_name: event.target.value })
                            }
                        />
                    </FormField>
                </div>

                <div className='grid gap-4 sm:grid-cols-2'>
                    <FormField htmlFor='auth-provider-client-id' label='Client ID' required>
                        <Input
                            autoComplete='off'
                            id='auth-provider-client-id'
                            value={draft.client_id}
                            variant='secondary'
                            onChange={(event) =>
                                setDraft({ ...draft, client_id: event.target.value })
                            }
                        />
                    </FormField>
                    <FormField
                        htmlFor='auth-provider-client-secret'
                        label='Client secret'
                        hint={
                            draft.client_secret_configured
                                ? 'Leave empty to keep the current secret.'
                                : undefined
                        }
                        required={!draft.client_secret_configured}
                    >
                        <Input
                            autoComplete='new-password'
                            id='auth-provider-client-secret'
                            placeholder={draft.client_secret_configured ? 'Configured' : undefined}
                            type='password'
                            value={secret}
                            variant='secondary'
                            onChange={(event) => setSecret(event.target.value)}
                        />
                    </FormField>
                </div>

                {draft.type === 'OIDC' ? (
                    <FormField
                        htmlFor='auth-provider-issuer'
                        label='Issuer URL'
                        hint='Use the exact issuer advertised by the provider discovery document.'
                        required
                    >
                        <Input
                            autoCapitalize='none'
                            id='auth-provider-issuer'
                            placeholder={preset.issuerPlaceholder}
                            type='url'
                            value={draft.issuer_url}
                            variant='secondary'
                            onChange={(event) =>
                                setDraft({ ...draft, issuer_url: event.target.value })
                            }
                        />
                    </FormField>
                ) : (
                    <div className='grid gap-4'>
                        <FormField
                            htmlFor='auth-provider-authorization-url'
                            label='Authorization URL'
                            required
                        >
                            <Input
                                autoCapitalize='none'
                                id='auth-provider-authorization-url'
                                type='url'
                                value={draft.authorization_url}
                                variant='secondary'
                                onChange={(event) =>
                                    setDraft({ ...draft, authorization_url: event.target.value })
                                }
                            />
                        </FormField>
                        <div className='grid gap-4 sm:grid-cols-2'>
                            <FormField htmlFor='auth-provider-token-url' label='Token URL' required>
                                <Input
                                    autoCapitalize='none'
                                    id='auth-provider-token-url'
                                    type='url'
                                    value={draft.token_url}
                                    variant='secondary'
                                    onChange={(event) =>
                                        setDraft({ ...draft, token_url: event.target.value })
                                    }
                                />
                            </FormField>
                            <FormField
                                htmlFor='auth-provider-user-info-url'
                                label='User info URL'
                                required
                            >
                                <Input
                                    autoCapitalize='none'
                                    id='auth-provider-user-info-url'
                                    type='url'
                                    value={draft.user_info_url}
                                    variant='secondary'
                                    onChange={(event) =>
                                        setDraft({ ...draft, user_info_url: event.target.value })
                                    }
                                />
                            </FormField>
                        </div>
                        <FormField
                            htmlFor='auth-provider-email-url'
                            label='Verified email URL'
                            hint='Optional. The endpoint must return a list with email, primary, and verified fields.'
                        >
                            <Input
                                autoCapitalize='none'
                                id='auth-provider-email-url'
                                type='url'
                                value={draft.email_url}
                                variant='secondary'
                                onChange={(event) =>
                                    setDraft({ ...draft, email_url: event.target.value })
                                }
                            />
                        </FormField>
                        {draft.preset === 'GENERIC_OAUTH2' && (
                            <Alert status='warning'>
                                <Alert.Indicator />
                                <Alert.Content>
                                    <Alert.Description>
                                        User info must return id or sub, email, name, and
                                        email_verified=true.
                                    </Alert.Description>
                                </Alert.Content>
                            </Alert>
                        )}
                    </div>
                )}

                <FormField
                    htmlFor='auth-provider-redirect-url'
                    label='Redirect URL'
                    hint='Register this exact URL in the provider application.'
                    required
                >
                    <Input
                        autoCapitalize='none'
                        id='auth-provider-redirect-url'
                        type='url'
                        value={draft.redirect_url}
                        variant='secondary'
                        onChange={(event) =>
                            setDraft({ ...draft, redirect_url: event.target.value })
                        }
                    />
                </FormField>

                <FormField
                    htmlFor='auth-provider-scopes'
                    label='Scopes'
                    hint='Separate scopes with commas.'
                >
                    <Input
                        id='auth-provider-scopes'
                        value={draft.scopes.join(', ')}
                        variant='secondary'
                        onChange={(event) =>
                            setDraft({
                                ...draft,
                                scopes: event.target.value
                                    .split(',')
                                    .map((scope) => scope.trim())
                                    .filter(Boolean),
                            })
                        }
                    />
                </FormField>

                <div className='divide-y divide-border rounded-lg border border-border px-4'>
                    <div className='flex items-start justify-between gap-4 py-3'>
                        <div>
                            <div className='text-sm font-medium'>Enabled</div>
                            <p className='mt-1 text-xs text-muted'>
                                Show this provider on sign-in.
                            </p>
                        </div>
                        <ToggleSwitch
                            isSelected={draft.enabled}
                            label='Enabled'
                            onChange={(enabled) => setDraft({ ...draft, enabled })}
                        />
                    </div>
                    <div className='flex items-start justify-between gap-4 py-3'>
                        <div>
                            <div className='text-sm font-medium'>Automatically create users</div>
                            <p className='mt-1 text-xs text-muted'>
                                Create viewer accounts for verified emails without a local match.
                            </p>
                        </div>
                        <ToggleSwitch
                            isDisabled={requireTOTP}
                            isSelected={draft.auto_create_users}
                            label='Automatically create users'
                            onChange={(autoCreateUsers) =>
                                setDraft({ ...draft, auto_create_users: autoCreateUsers })
                            }
                        />
                    </div>
                </div>
            </div>
            <DialogFooter>
                <Button type='button' variant='ghost' onPress={onClose}>
                    Cancel
                </Button>
                <Button type='button' variant='primary' onPress={submit}>
                    {editing ? 'Apply changes' : 'Add provider'}
                </Button>
            </DialogFooter>
        </DialogShell>
    );
}
