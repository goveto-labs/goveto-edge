import type { AuthMethods, TOTPSetup } from '@/api';

import { Alert, Button, Input, InputOTP, useOverlayState } from '@heroui/react';
import { Copy, KeyRound, Loader2, RefreshCw, ShieldCheck, ShieldOff } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import { ApiError, authApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useAuth } from '@/hooks/useAuth.ts';

type VerificationAction = 'disable' | 'recovery';

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

export default function Settings() {
    const { user, refresh } = useAuth();
    const [searchParams, setSearchParams] = useSearchParams();
    const setupDialog = useOverlayState();
    const verificationDialog = useOverlayState();
    const recoveryDialog = useOverlayState();
    const [setup, setSetup] = useState<TOTPSetup | null>(null);
    const [password, setPassword] = useState('');
    const [code, setCode] = useState('');
    const [verificationAction, setVerificationAction] = useState<VerificationAction>('disable');
    const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');
    const [success, setSuccess] = useState('');
    const [copied, setCopied] = useState(false);
    const [authMethods, setAuthMethods] = useState<AuthMethods | null>(null);
    const [linkingProvider, setLinkingProvider] = useState('');

    useEffect(() => {
        let active = true;
        authApi
            .methods()
            .then((methods) => {
                if (active) setAuthMethods(methods);
            })
            .catch(() => undefined);
        return () => {
            active = false;
        };
    }, []);

    useEffect(() => {
        const authError = searchParams.get('auth_error');
        const linkSucceeded = searchParams.get('external_link') === 'success';
        if (authError) setError(authError);
        if (linkSucceeded) setSuccess('External identity linked.');
        if (!authError && !linkSucceeded) return;
        const next = new URLSearchParams(searchParams);
        next.delete('external_link');
        next.delete('auth_error');
        setSearchParams(next, { replace: true });
    }, [searchParams, setSearchParams]);

    const linkExternalProvider = async (providerId: string) => {
        setLinkingProvider(providerId);
        setError('');
        setSuccess('');
        try {
            const result = await authApi.linkExternalProvider(providerId);
            window.location.assign(result.authorization_url);
        } catch (linkError) {
            setError(errorMessage(linkError, 'Failed to start external identity link'));
            setLinkingProvider('');
        }
    };

    const clearCredentials = () => {
        setPassword('');
        setCode('');
        setError('');
    };

    const beginEnable = async () => {
        setBusy(true);
        setError('');
        setSuccess('');
        try {
            setSetup(await authApi.setupTOTP());
            clearCredentials();
            setupDialog.open();
        } catch (setupError) {
            setError(errorMessage(setupError, 'Failed to begin two-factor setup'));
        } finally {
            setBusy(false);
        }
    };

    const enable = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!setup) return;
        setBusy(true);
        setError('');
        try {
            const result = await authApi.enableTOTP({
                password,
                code,
                secret: setup.secret,
            });
            await refresh(true);
            setupDialog.close();
            setRecoveryCodes(result.recovery_codes);
            setCopied(false);
            clearCredentials();
            recoveryDialog.open();
            setSuccess('Two-factor authentication is enabled.');
        } catch (enableError) {
            setError(errorMessage(enableError, 'Failed to enable two-factor authentication'));
        } finally {
            setBusy(false);
        }
    };

    const openVerification = (action: VerificationAction) => {
        clearCredentials();
        setSuccess('');
        setVerificationAction(action);
        verificationDialog.open();
    };

    const verify = async (event: React.FormEvent) => {
        event.preventDefault();
        setBusy(true);
        setError('');
        try {
            if (verificationAction === 'disable') {
                await authApi.disableTOTP({ password, code });
                await refresh(true);
                verificationDialog.close();
                setSuccess('Two-factor authentication is disabled.');
            } else {
                const result = await authApi.regenerateTOTPRecoveryCodes({ password, code });
                verificationDialog.close();
                setRecoveryCodes(result.recovery_codes);
                setCopied(false);
                recoveryDialog.open();
                setSuccess('New recovery codes generated. Previous codes no longer work.');
            }
            clearCredentials();
        } catch (verificationError) {
            setError(errorMessage(verificationError, 'Verification failed'));
        } finally {
            setBusy(false);
        }
    };

    const copyRecoveryCodes = async () => {
        try {
            await navigator.clipboard.writeText(recoveryCodes.join('\n'));
            setCopied(true);
        } catch (copyError) {
            setError(errorMessage(copyError, 'Unable to copy recovery codes'));
        }
    };

    const copySetupKey = async () => {
        if (!setup) return;
        try {
            await navigator.clipboard.writeText(setup.secret);
        } catch (copyError) {
            setError(errorMessage(copyError, 'Unable to copy the setup key'));
        }
    };

    return (
        <div className='mx-auto max-w-3xl space-y-5'>
            <PageHeader subtitle='Manage security for your account.' title='Settings' />

            {error && !setupDialog.isOpen && !verificationDialog.isOpen && (
                <FormError message={error} />
            )}
            {success && (
                <Alert status='success'>
                    <Alert.Indicator />
                    <Alert.Content>
                        <Alert.Title>Security updated</Alert.Title>
                        <Alert.Description>{success}</Alert.Description>
                    </Alert.Content>
                </Alert>
            )}

            <ContentCard noPadding>
                <div className='flex flex-col gap-4 px-4 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-5'>
                    <div className='min-w-0'>
                        <div className='flex items-center gap-2'>
                            <h2 className='text-sm font-semibold'>Two-factor authentication</h2>
                            <span className='flex items-center gap-1.5 text-xs text-muted'>
                                <span
                                    className={`h-1.5 w-1.5 rounded-full ${
                                        user?.totp_enabled ? 'bg-success' : 'bg-muted'
                                    }`}
                                />
                                {user?.totp_enabled ? 'Enabled' : 'Not enabled'}
                            </span>
                        </div>
                        <p className='mt-1 max-w-xl text-xs leading-5 text-muted'>
                            Use an authenticator app for an additional verification step when you
                            sign in.
                            {user?.totp_required &&
                                ' This instance requires two-factor authentication.'}
                        </p>
                    </div>

                    <div className='flex shrink-0 flex-wrap items-center gap-2'>
                        {user?.totp_enabled ? (
                            <>
                                <Button
                                    isDisabled={busy}
                                    size='sm'
                                    variant='secondary'
                                    onPress={() => openVerification('recovery')}
                                >
                                    <RefreshCw aria-hidden='true' className='h-4 w-4' />
                                    Recovery codes
                                </Button>
                                <Button
                                    isDisabled={busy || user.totp_required}
                                    size='sm'
                                    variant='danger'
                                    onPress={() => openVerification('disable')}
                                >
                                    <ShieldOff aria-hidden='true' className='h-4 w-4' />
                                    Disable
                                </Button>
                            </>
                        ) : (
                            <Button
                                isDisabled={busy}
                                size='sm'
                                variant='primary'
                                onPress={() => void beginEnable()}
                            >
                                {busy ? (
                                    <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                                ) : (
                                    <ShieldCheck aria-hidden='true' className='h-4 w-4' />
                                )}
                                Enable 2FA
                            </Button>
                        )}
                    </div>
                </div>
            </ContentCard>

            {authMethods && authMethods.providers.length > 0 && (
                <ContentCard noPadding>
                    <div className='flex flex-col gap-4 px-4 py-4 sm:px-5'>
                        <div>
                            <h2 className='text-sm font-semibold'>External identities</h2>
                            <p className='mt-1 text-xs leading-5 text-muted'>
                                Connect a verified sign-in provider to this account.
                            </p>
                        </div>
                        <div className='divide-y divide-border rounded-lg border border-border'>
                            {authMethods.providers.map((provider) => (
                                <div
                                    className='flex min-h-14 items-center justify-between gap-4 px-3 py-2.5'
                                    key={provider.id}
                                >
                                    <div className='min-w-0'>
                                        <div className='truncate text-sm font-medium'>
                                            {provider.provider_name}
                                        </div>
                                        <div className='text-xs text-muted'>{provider.type}</div>
                                    </div>
                                    <Button
                                        isDisabled={linkingProvider !== ''}
                                        size='sm'
                                        variant='secondary'
                                        onPress={() => void linkExternalProvider(provider.id)}
                                    >
                                        {linkingProvider === provider.id ? (
                                            <Loader2
                                                aria-hidden='true'
                                                className='h-4 w-4 animate-spin'
                                            />
                                        ) : (
                                            <KeyRound aria-hidden='true' className='h-4 w-4' />
                                        )}
                                        Link
                                    </Button>
                                </div>
                            ))}
                        </div>
                    </div>
                </ContentCard>
            )}

            <DialogShell
                icon={<ShieldCheck className='h-5 w-5' />}
                isOpen={setupDialog.isOpen}
                size='sm'
                subtitle='Add the setup key to your authenticator, then verify the first code.'
                title='Enable two-factor authentication'
                onOpenChange={(open) => {
                    setupDialog.setOpen(open);
                    if (!open) clearCredentials();
                }}
            >
                <form onSubmit={enable}>
                    <div className='space-y-4 p-6'>
                        {error && <FormError message={error} />}
                        {setup && (
                            <div className='space-y-1.5'>
                                <div className='text-sm font-medium'>Authenticator setup key</div>
                                <div className='flex items-center gap-2 rounded-lg border border-border bg-surface-secondary px-3 py-2'>
                                    <code className='min-w-0 flex-1 break-all text-xs'>
                                        {setup.secret}
                                    </code>
                                    <Button
                                        aria-label='Copy setup key'
                                        isIconOnly
                                        size='sm'
                                        type='button'
                                        variant='ghost'
                                        onPress={() => void copySetupKey()}
                                    >
                                        <Copy aria-hidden='true' className='h-4 w-4' />
                                    </Button>
                                </div>
                                <a className='text-xs text-accent hover:underline' href={setup.uri}>
                                    Open in authenticator app
                                </a>
                            </div>
                        )}
                        <FormField htmlFor='totp-enable-password' label='Current password' required>
                            <Input
                                autoComplete='current-password'
                                id='totp-enable-password'
                                required
                                variant='secondary'
                                type='password'
                                value={password}
                                onChange={(event) => setPassword(event.target.value)}
                            />
                        </FormField>
                        <FormField label='Verification code' required>
                            <div className='flex justify-center py-1'>
                                <InputOTP
                                    maxLength={6}
                                    value={code}
                                    variant='secondary'
                                    onChange={setCode}
                                >
                                    <InputOTP.Group>
                                        <InputOTP.Slot index={0} />
                                        <InputOTP.Slot index={1} />
                                        <InputOTP.Slot index={2} />
                                    </InputOTP.Group>
                                    <InputOTP.Separator />
                                    <InputOTP.Group>
                                        <InputOTP.Slot index={3} />
                                        <InputOTP.Slot index={4} />
                                        <InputOTP.Slot index={5} />
                                    </InputOTP.Group>
                                </InputOTP>
                            </div>
                        </FormField>
                    </div>
                    <DialogFooter>
                        <Button type='button' variant='ghost' onPress={setupDialog.close}>
                            Cancel
                        </Button>
                        <Button
                            isDisabled={busy || !password || code.length !== 6}
                            type='submit'
                            variant='primary'
                        >
                            {busy && (
                                <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                            )}
                            Enable 2FA
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <DialogShell
                icon={<KeyRound className='h-5 w-5' />}
                isOpen={verificationDialog.isOpen}
                size='sm'
                subtitle='Confirm your identity before changing two-factor authentication.'
                title={
                    verificationAction === 'disable'
                        ? 'Disable two-factor authentication'
                        : 'Generate new recovery codes'
                }
                onOpenChange={(open) => {
                    verificationDialog.setOpen(open);
                    if (!open) clearCredentials();
                }}
            >
                <form onSubmit={verify}>
                    <div className='space-y-4 p-6'>
                        {error && <FormError message={error} />}
                        <FormField htmlFor='totp-verify-password' label='Current password' required>
                            <Input
                                autoComplete='current-password'
                                id='totp-verify-password'
                                required
                                type='password'
                                value={password}
                                onChange={(event) => setPassword(event.target.value)}
                            />
                        </FormField>
                        <FormField htmlFor='totp-verify-code' label='Current code' required>
                            <Input
                                autoComplete='one-time-code'
                                id='totp-verify-code'
                                placeholder='Authenticator or recovery code'
                                required
                                spellCheck={false}
                                value={code}
                                onChange={(event) => setCode(event.target.value)}
                            />
                        </FormField>
                    </div>
                    <DialogFooter>
                        <Button type='button' variant='ghost' onPress={verificationDialog.close}>
                            Cancel
                        </Button>
                        <Button
                            isDisabled={busy || !password || !code.trim()}
                            type='submit'
                            variant={verificationAction === 'disable' ? 'danger' : 'primary'}
                        >
                            {busy && (
                                <Loader2 aria-hidden='true' className='h-4 w-4 animate-spin' />
                            )}
                            {verificationAction === 'disable' ? 'Disable 2FA' : 'Generate codes'}
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <DialogShell
                icon={<KeyRound className='h-5 w-5' />}
                isDismissable={false}
                isOpen={recoveryDialog.isOpen}
                size='sm'
                subtitle='Store these one-time codes somewhere secure. They will not be shown again.'
                title='Recovery codes'
                onOpenChange={recoveryDialog.setOpen}
            >
                <div className='space-y-4 p-6'>
                    {error && <FormError message={error} />}
                    <div className='grid grid-cols-2 gap-x-5 gap-y-2 rounded-lg border border-border bg-surface-secondary px-4 py-3 font-mono text-sm'>
                        {recoveryCodes.map((recoveryCode) => (
                            <code key={recoveryCode}>{recoveryCode}</code>
                        ))}
                    </div>
                </div>
                <DialogFooter>
                    <Button
                        type='button'
                        variant='secondary'
                        onPress={() => void copyRecoveryCodes()}
                    >
                        <Copy aria-hidden='true' className='h-4 w-4' />
                        {copied ? 'Copied' : 'Copy codes'}
                    </Button>
                    <Button type='button' variant='primary' onPress={recoveryDialog.close}>
                        Done
                    </Button>
                </DialogFooter>
            </DialogShell>
        </div>
    );
}
