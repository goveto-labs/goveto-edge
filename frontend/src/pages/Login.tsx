import { Button, Card, Input, Label } from '@heroui/react';
import { Globe, Loader2 } from 'lucide-react';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';

import { ApiError } from '@/api';
import { useAuth } from '@/hooks/useAuth.ts';

export default function Login() {
    const navigate = useNavigate();
    const { login } = useAuth();
    const [email, setEmail] = useState('');
    const [password, setPassword] = useState('');
    const [code, setCode] = useState('');
    const [error, setError] = useState('');
    const [loading, setLoading] = useState(false);

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        setError('');
        setLoading(true);
        try {
            await login({ email, password, code });
            navigate('/');
        } catch (err) {
            const message =
                err instanceof ApiError
                    ? err.message
                    : err instanceof Error
                      ? err.message
                      : 'Login failed';
            setError(message);
        } finally {
            setLoading(false);
        }
    };

    return (
        <div className='flex min-h-screen items-center justify-center bg-background p-4'>
            <Card className='w-full max-w-md p-6 shadow-sm'>
                <div className='mb-6 flex items-center gap-2'>
                    <div className='flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-foreground'>
                        <Globe className='h-5 w-5' />
                    </div>
                    <span className='text-lg font-bold'>Goveto Edge</span>
                </div>

                <h1 className='text-2xl font-bold'>Sign in</h1>
                <p className='mb-6 text-sm text-muted'>Enter your credentials to continue.</p>

                {error && (
                    <div className='mb-4 rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                        {error}
                    </div>
                )}

                <form className='space-y-4' onSubmit={handleSubmit}>
                    <div>
                        <Label htmlFor='login-email'>Email</Label>
                        <Input
                            id='login-email'
                            required
                            type='email'
                            value={email}
                            onChange={(e) => setEmail(e.target.value)}
                        />
                    </div>
                    <div>
                        <Label htmlFor='login-password'>Password</Label>
                        <Input
                            id='login-password'
                            required
                            type='password'
                            value={password}
                            onChange={(e) => setPassword(e.target.value)}
                        />
                    </div>
                    <div>
                        <Label htmlFor='login-code'>2FA code (optional)</Label>
                        <Input
                            id='login-code'
                            value={code}
                            onChange={(e) => setCode(e.target.value)}
                        />
                    </div>
                    <Button fullWidth isDisabled={loading} type='submit' variant='primary'>
                        {loading ? (
                            <span className='flex items-center justify-center gap-2'>
                                <Loader2 className='h-4 w-4 animate-spin' />
                                Signing in...
                            </span>
                        ) : (
                            'Sign in'
                        )}
                    </Button>
                </form>

                <p className='mt-4 text-center text-sm text-muted'>
                    No account?{' '}
                    <Link className='text-accent hover:underline' to='/register'>
                        Register
                    </Link>
                </p>
            </Card>
        </div>
    );
}
