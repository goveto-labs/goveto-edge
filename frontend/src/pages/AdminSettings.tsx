import { Alert, Button, Input, Spinner } from '@heroui/react';
import { AlertTriangle, RotateCw } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { Navigate } from 'react-router-dom';

import { ApiError, adminSettingsApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { FormError } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useAuth } from '@/hooks/useAuth.ts';

function errorMessage(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

export default function AdminSettings() {
    const { user, loading: authLoading } = useAuth();
    const [address, setAddress] = useState('');
    const [savedAddress, setSavedAddress] = useState('');
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [restarting, setRestarting] = useState(false);
    const [saved, setSaved] = useState(false);
    const [error, setError] = useState('');
    const dirty = useMemo(
        () => Boolean(savedAddress) && address.trim() !== savedAddress,
        [address, savedAddress]
    );

    useEffect(() => {
        if (!user?.is_instance_owner) return;
        let active = true;
        setLoading(true);
        adminSettingsApi
            .get()
            .then((settings) => {
                if (!active) return;
                setAddress(settings.agent_gateway_public_address);
                setSavedAddress(settings.agent_gateway_public_address);
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
    }, [user?.is_instance_owner]);

    if (!authLoading && !user?.is_instance_owner) {
        return <Navigate replace to='/' />;
    }

    const saveAndRestart = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!dirty) return;
        setSaving(true);
        setError('');
        try {
            const settings = await adminSettingsApi.update({
                agent_gateway_public_address: address,
                restart: true,
            });
            setAddress(settings.agent_gateway_public_address);
            setSavedAddress(settings.agent_gateway_public_address);
            setRestarting(settings.restarting);
            setSaved(!settings.restarting);
        } catch (saveError) {
            setError(errorMessage(saveError, 'Failed to save admin settings'));
        } finally {
            setSaving(false);
        }
    };

    return (
        <div className='mx-auto max-w-3xl space-y-5'>
            <PageHeader
                subtitle='Instance-wide settings available only to the instance owner.'
                title='Admin settings'
            />

            {error && <FormError message={error} />}
            {restarting && (
                <Alert status='success'>
                    <Alert.Indicator />
                    <Alert.Content>
                        <Alert.Title>Restart requested</Alert.Title>
                        <Alert.Description>
                            The control plane is shutting down gracefully. Its service supervisor
                            must start it again before this page can reconnect.
                        </Alert.Description>
                    </Alert.Content>
                </Alert>
            )}
            {saved && (
                <Alert status='success'>
                    <Alert.Indicator />
                    <Alert.Content>
                        <Alert.Title>No restart needed</Alert.Title>
                        <Alert.Description>
                            The normalized address already matches the active setting.
                        </Alert.Description>
                    </Alert.Content>
                </Alert>
            )}

            <ContentCard noPadding>
                {loading || authLoading ? (
                    <div className='flex min-h-28 items-center justify-center'>
                        <Spinner />
                    </div>
                ) : (
                    <form onSubmit={saveAndRestart}>
                        <div className='grid gap-2 px-4 py-4 sm:px-5 md:items-start'>
                            <div>
                                <label
                                    className='text-sm font-semibold text-foreground'
                                    htmlFor='agent-gateway-public-address'
                                >
                                    Agent gateway public address
                                </label>
                                <p className='mt-1 text-xs leading-5 text-muted'>
                                    Host and port that edge nodes use for their mTLS control
                                    channel.
                                </p>
                            </div>
                            <div className='space-y-1.5'>
                                <Input
                                    autoCapitalize='none'
                                    autoComplete='off'
                                    id='agent-gateway-public-address'
                                    placeholder='edge.example.com:8443'
                                    variant='secondary'
                                    className={"w-full"}
                                    required
                                    spellCheck={false}
                                    value={address}
                                    onChange={(event) => {
                                        setAddress(event.target.value);
                                        setRestarting(false);
                                        setSaved(false);
                                    }}
                                />
                                <p className='text-xs leading-5 text-muted'>
                                    Use host:port without a URL scheme or path. IPv6 addresses
                                    require brackets.
                                </p>
                            </div>
                        </div>

                        {dirty && (
                            <div className='flex flex-col gap-3 border-t border-border bg-surface-secondary/30 px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5'>
                                <div className='flex min-w-0 items-start gap-2 text-xs text-muted'>
                                    <AlertTriangle className='mt-0.5 h-4 w-4 shrink-0 text-warning' />
                                    <span>
                                        Existing agents keep their old address until they are
                                        updated or reinstalled.
                                    </span>
                                </div>
                                <Button
                                    className='shrink-0'
                                    isDisabled={saving || !address.trim()}
                                    type='submit'
                                    variant='primary'
                                >
                                    <RotateCw
                                        className={`h-4 w-4 ${saving ? 'animate-spin' : ''}`}
                                    />
                                    {saving ? 'Saving…' : 'Save and restart'}
                                </Button>
                            </div>
                        )}
                    </form>
                )}
            </ContentCard>
        </div>
    );
}
