import type { RegistrationConfig } from '@/api';

import { Button, Card, Input, Label, Spinner } from '@heroui/react';
import { Globe, Loader2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';

import { ApiError, authApi } from '@/api';
import { useAuth } from '@/hooks/useAuth.ts';

export default function Register() {
    const navigate = useNavigate();
    const { register } = useAuth();
    const [config, setConfig] = useState<RegistrationConfig | null>(null);
    const [loadingConfig, setLoadingConfig] = useState(true);
    const [name, setName] = useState('');
    const [email, setEmail] = useState('');
    const [password, setPassword] = useState('');
    const [captchaToken, setCaptchaToken] = useState('');
    const [error, setError] = useState('');
    const [success, setSuccess] = useState(false);
    const [loading, setLoading] = useState(false);

    useEffect(() => {
        authApi
            .registrationConfig()
            .then(setConfig)
            .catch(() => setConfig({ enabled: false }))
            .finally(() => setLoadingConfig(false));
    }, []);

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        setError('');
        setLoading(true);
        try {
            await register({
                name,
                email,
                password,
                captcha_token: captchaToken,
            });
            setSuccess(true);
        } catch (err) {
            const message =
                err instanceof ApiError
                    ? err.message
                    : err instanceof Error
                      ? err.message
                      : 'Registration failed';
            setError(message);
        } finally {
            setLoading(false);
        }
    };

    if (loadingConfig) {
        return (
            <div className='flex h-screen items-center justify-center'>
                <Spinner />
            </div>
        );
    }

    if (config && !config.enabled) {
        return (
            <div className='flex min-h-screen items-center justify-center p-4'>
                <Card className='w-full max-w-md p-6'>
                    <div className='mb-4 flex items-center gap-2'>
                        <div className='flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-foreground'>
                            <Globe className='h-5 w-5' />
                        </div>
                        <span className='text-lg font-bold'>Goveto Edge</span>
                    </div>
                    <h1 className='text-xl font-bold'>Registration disabled</h1>
                    <p className='mt-2 text-sm text-muted'>
                        Public registration is currently disabled. Contact an administrator for an
                        account.
                    </p>
                    <Button className='mt-4' fullWidth onPress={() => navigate('/login')}>
                        Back to login
                    </Button>
                </Card>
            </div>
        );
    }

    return (
        <div className='flex min-h-screen items-center justify-center bg-background p-4'>
            <Card className='w-full max-w-md p-6 shadow-sm'>
                <div className='mb-6 flex items-center gap-2'>
                    <div className='flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-foreground'>
                        <Globe className='h-5 w-5' />
                    </div>
                    <span className='text-lg font-bold'>Goveto Edge</span>
                </div>

                <h1 className='text-2xl font-bold'>Create account</h1>
                <p className='mb-6 text-sm text-muted'>Register to access the control plane.</p>

                {success ? (
                    <div className='space-y-4'>
                        <div className='rounded-lg bg-success px-4 py-3 text-sm text-success-foreground'>
                            Registration successful. You can now sign in.
                        </div>
                        <Button fullWidth onPress={() => navigate('/login')}>
                            Go to login
                        </Button>
                    </div>
                ) : (
                    <form className='space-y-4' onSubmit={handleSubmit}>
                        {error && (
                            <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                                {error}
                            </div>
                        )}
                        <div>
                            <Label htmlFor='register-name'>Name</Label>
                            <Input
                                id='register-name'
                                required
                                value={name}
                                onChange={(e) => setName(e.target.value)}
                            />
                        </div>
                        <div>
                            <Label htmlFor='register-email'>Email</Label>
                            <Input
                                id='register-email'
                                required
                                type='email'
                                value={email}
                                onChange={(e) => setEmail(e.target.value)}
                            />
                        </div>
                        <div>
                            <Label htmlFor='register-password'>Password</Label>
                            <Input
                                id='register-password'
                                required
                                type='password'
                                value={password}
                                onChange={(e) => setPassword(e.target.value)}
                            />
                        </div>
                        <div>
                            <Label htmlFor='register-captcha'>Captcha token</Label>
                            <Input
                                id='register-captcha'
                                required
                                value={captchaToken}
                                onChange={(e) => setCaptchaToken(e.target.value)}
                            />
                        </div>
                        <Button fullWidth isDisabled={loading} type='submit' variant='primary'>
                            {loading ? (
                                <span className='flex items-center justify-center gap-2'>
                                    <Loader2 className='h-4 w-4 animate-spin' />
                                    Creating account...
                                </span>
                            ) : (
                                'Register'
                            )}
                        </Button>
                    </form>
                )}

                <p className='mt-4 text-center text-sm text-muted'>
                    Already have an account?{' '}
                    <Link className='text-accent hover:underline' to='/login'>
                        Sign in
                    </Link>
                </p>
            </Card>
        </div>
    );
}
