import { Button, Input, Label } from '@heroui/react';
import { CheckCircle2, Globe, Loader2 } from 'lucide-react';
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, initializationApi } from '@/api';
import { useInitialization } from '@/hooks/useInitialization.tsx';

export default function Init() {
    const navigate = useNavigate();
    const { complete } = useInitialization();
    const [name, setName] = useState('');
    const [email, setEmail] = useState('');
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
            await initializationApi.initialize({ name, email, password });
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
        <div className='grid min-h-screen'>
            <main className='relative flex flex-col justify-center bg-background px-6 py-12 sm:px-10 lg:px-16'>
                <div className='mx-auto w-full max-w-[380px]'>
                    <div className='mb-10 flex items-center gap-2.5'>
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

                    {error && (
                        <div
                            className='mb-5 rounded-xl border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger-foreground'
                            role='alert'
                        >
                            {error}
                        </div>
                    )}

                    <form className='flex flex-col gap-4' onSubmit={handleSubmit}>
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='init-name'>Administrator name</Label>
                            <Input
                                autoFocus
                                id='init-name'
                                placeholder='Your name'
                                required
                                value={name}
                                onChange={(event) => setName(event.target.value)}
                            />
                        </div>
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='init-email'>Email</Label>
                            <Input
                                autoComplete='email'
                                id='init-email'
                                placeholder='admin@example.com'
                                required
                                type='email'
                                value={email}
                                onChange={(event) => setEmail(event.target.value)}
                            />
                        </div>
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='init-password'>Password</Label>
                            <Input
                                autoComplete='new-password'
                                id='init-password'
                                minLength={10}
                                required
                                type='password'
                                value={password}
                                onChange={(event) => setPassword(event.target.value)}
                            />
                        </div>
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='init-confirm-password'>Confirm password</Label>
                            <Input
                                autoComplete='new-password'
                                id='init-confirm-password'
                                minLength={10}
                                required
                                type='password'
                                value={confirmPassword}
                                onChange={(event) => setConfirmPassword(event.target.value)}
                            />
                        </div>
                        <p className='flex items-center gap-2 text-xs text-muted'>
                            <CheckCircle2 className='h-3.5 w-3.5 text-success' />
                            Use at least 10 characters for the administrator password.
                        </p>
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
