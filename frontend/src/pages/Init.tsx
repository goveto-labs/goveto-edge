import { Button, Input } from '@heroui/react';
import { Globe, Loader2 } from 'lucide-react';
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, initializationApi } from '@/api';
import { FormError, FormField } from '@/components/FormField.tsx';
import { useInitialization } from '@/hooks/useInitialization.tsx';
import { useSystemTheme } from '@/hooks/useSystemTheme.ts';

export default function Init() {
    useSystemTheme();
    const navigate = useNavigate();
    const { complete } = useInitialization();
    const [name, setName] = useState('');
    const [email, setEmail] = useState('');
    const [agentGatewayPublicAddress, setAgentGatewayPublicAddress] = useState('');
    const [password, setPassword] = useState('');
    const [confirmPassword, setConfirmPassword] = useState('');
    const [error, setError] = useState('');
    const [loading, setLoading] = useState(false);

    const handleSubmit = async (event: React.FormEvent) => {
        event.preventDefault();
        setError('');
        if (password !== confirmPassword) {
            setError('Passwords do not match.');
            return;
        }

        setLoading(true);
        try {
            await initializationApi.initialize({
                name,
                email,
                password,
                agent_gateway_public_address: agentGatewayPublicAddress,
            });
            complete();
            navigate('/login', { replace: true });
        } catch (err) {
            setError(
                err instanceof ApiError
                    ? err.message
                    : err instanceof Error
                      ? err.message
                      : 'Initialization failed'
            );
        } finally {
            setLoading(false);
        }
    };

    return (
        <div className='grid h-[100%] bg-background text-foreground'>
            <main className='relative flex flex-col overflow-y-auto bg-background px-6 py-12 sm:px-10 lg:px-16'>
                <div className='mx-auto my-auto w-full max-w-[460px]'>
                    <div className='mb-10 flex items-center gap-2.5 lg:hidden'>
                        <div className='flex h-9 w-9 items-center justify-center rounded-xl bg-accent text-accent-foreground'>
                            <Globe className='h-4 w-4' />
                        </div>
                        <span className='text-lg font-semibold tracking-tight'>Goveto Edge</span>
                    </div>

                    <div className='mb-8 space-y-2'>
                        <h2 className='text-2xl font-semibold tracking-tight sm:text-[1.75rem]'>
                            Initialize this instance
                        </h2>
                        <p className='text-sm text-muted'>
                            Create the first administrator account to continue.
                        </p>
                    </div>

                    {error && <FormError className='mb-5' message={error} />}

                    <form className='flex flex-col gap-4' onSubmit={handleSubmit}>
                        <FormField htmlFor='init-name' label='Administrator name' required>
                            <Input
                                autoFocus
                                autoComplete='name'
                                id='init-name'
                                placeholder='Your name'
                                required
                                value={name}
                                onChange={(event) => setName(event.target.value)}
                            />
                        </FormField>
                        <FormField htmlFor='init-email' label='Email' required>
                            <Input
                                autoComplete='email'
                                id='init-email'
                                placeholder='admin@example.com'
                                required
                                type='email'
                                value={email}
                                onChange={(event) => setEmail(event.target.value)}
                            />
                        </FormField>
                        <FormField
                            hint='Use at least 10 characters for the administrator password.'
                            htmlFor='init-password'
                            label='Password'
                            required
                        >
                            <Input
                                autoComplete='new-password'
                                id='init-password'
                                minLength={10}
                                placeholder='••••••••••'
                                required
                                type='password'
                                value={password}
                                onChange={(event) => setPassword(event.target.value)}
                            />
                        </FormField>
                        <FormField
                            htmlFor='init-confirm-password'
                            label='Confirm password'
                            required
                        >
                            <Input
                                autoComplete='new-password'
                                id='init-confirm-password'
                                minLength={10}
                                placeholder='••••••••••'
                                required
                                type='password'
                                value={confirmPassword}
                                onChange={(event) => setConfirmPassword(event.target.value)}
                            />
                        </FormField>
                        <FormField
                            hint='Use the hostname or public IP edge nodes can reach, followed by the gateway port. Do not include http://, https://, or a path.'
                            htmlFor='init-agent-gateway-address'
                            label='Agent gateway public address'
                            required
                        >
                            <Input
                                autoCapitalize='none'
                                autoComplete='off'
                                id='init-agent-gateway-address'
                                placeholder='edge.example.com:8443'
                                required
                                spellCheck={false}
                                value={agentGatewayPublicAddress}
                                onChange={(event) =>
                                    setAgentGatewayPublicAddress(event.target.value)
                                }
                            />
                        </FormField>
                        <Button fullWidth isDisabled={loading} type='submit' variant='primary'>
                            {loading ? (
                                <span className='flex items-center justify-center gap-2'>
                                    <Loader2 className='h-4 w-4 animate-spin' />
                                    Initializing…
                                </span>
                            ) : (
                                'Create administrator and continue'
                            )}
                        </Button>
                    </form>
                </div>

                <p className='absolute bottom-6 left-0 right-0 text-center text-xs text-muted/70 lg:left-auto lg:right-10 lg:text-right'>
                    First-time setup
                </p>
            </main>
        </div>
    );
}
