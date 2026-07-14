import { Button, Input, InputOTP, Label, Modal, useOverlayState } from '@heroui/react';
import { Globe, Loader2 } from 'lucide-react';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';

import { ApiError } from '@/api';
import { useAuth } from '@/hooks/useAuth.ts';

function isTotpRequired(err: unknown): boolean {
    return (
        err instanceof ApiError &&
        err.status === 401 &&
        err.message.toLowerCase().includes('totp code is required')
    );
}

export default function Login() {
    const navigate = useNavigate();
    const { login } = useAuth();
    const otpModal = useOverlayState();
    const [email, setEmail] = useState('');
    const [password, setPassword] = useState('');
    const [code, setCode] = useState('');
    const [error, setError] = useState('');
    const [otpError, setOtpError] = useState('');
    const [loading, setLoading] = useState(false);

    const completeLogin = async (otp?: string) => {
        await login({ email, password, code: otp });
        otpModal.close();
        navigate('/');
    };

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        setError('');
        setOtpError('');
        setLoading(true);
        try {
            await completeLogin();
        } catch (err) {
            if (isTotpRequired(err)) {
                setCode('');
                setOtpError('');
                otpModal.open();
            } else {
                setError(
                    err instanceof ApiError
                        ? err.message
                        : err instanceof Error
                          ? err.message
                          : 'Login failed'
                );
            }
        } finally {
            setLoading(false);
        }
    };

    const handleOtpSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (code.length !== 6) {
            setOtpError('Enter the 6-digit code from your authenticator app.');
            return;
        }
        setOtpError('');
        setLoading(true);
        try {
            await completeLogin(code);
        } catch (err) {
            setOtpError(
                err instanceof ApiError
                    ? err.message
                    : err instanceof Error
                      ? err.message
                      : 'Invalid code'
            );
        } finally {
            setLoading(false);
        }
    };

    return (
        <div className='grid min-h-screen'>
            <main className='relative flex flex-col justify-center bg-background px-6 py-12 sm:px-10 lg:px-16'>
                <div className='mx-auto w-full max-w-[380px]'>
                    <div className='mb-10 flex items-center gap-2.5 lg:hidden'>
                        <div className='flex h-9 w-9 items-center justify-center rounded-xl bg-accent text-accent-foreground'>
                            <Globe className='h-4 w-4' />
                        </div>
                        <span className='text-lg font-semibold tracking-tight'>Goveto Edge</span>
                    </div>

                    <div className='mb-8 space-y-2'>
                        <h2 className='text-2xl font-semibold tracking-tight sm:text-[1.75rem]'>
                            Sign in
                        </h2>
                        <p className='text-sm text-muted'>
                            Use your control plane credentials to continue.
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
                            <Label htmlFor='login-email'>Email</Label>
                            <Input
                                autoComplete='email'
                                autoFocus
                                id='login-email'
                                placeholder='you@company.com'
                                required
                                type='email'
                                value={email}
                                onChange={(e) => setEmail(e.target.value)}
                            />
                        </div>
                        <div className='flex flex-col gap-1'>
                            <Label htmlFor='login-password'>Password</Label>
                            <Input
                                autoComplete='current-password'
                                id='login-password'
                                placeholder='••••••••'
                                required
                                type='password'
                                value={password}
                                onChange={(e) => setPassword(e.target.value)}
                            />
                        </div>

                        <Button fullWidth isDisabled={loading} type='submit' variant='primary'>
                            {loading && !otpModal.isOpen ? (
                                <span className='flex items-center justify-center gap-2'>
                                    <Loader2 className='h-4 w-4 animate-spin' />
                                    Signing in…
                                </span>
                            ) : (
                                'Sign in'
                            )}
                        </Button>
                    </form>

                    <p className='mt-8 text-center text-sm text-muted'>
                        No account?{' '}
                        <Link
                            className='font-medium text-accent underline-offset-4 transition-colors hover:underline'
                            to='/register'
                        >
                            Register
                        </Link>
                    </p>
                </div>

                <p className='absolute bottom-6 left-0 right-0 text-center text-xs text-muted/70 lg:left-auto lg:right-10 lg:text-right'>
                    Edge delivery control plane
                </p>
            </main>

            <Modal
                isOpen={otpModal.isOpen}
                onOpenChange={(open) => {
                    if (!open) {
                        setCode('');
                        setOtpError('');
                    }
                    otpModal.setOpen(open);
                }}
            >
                <Modal.Backdrop>
                    <Modal.Container size='sm'>
                        <Modal.Dialog>
                            <form onSubmit={handleOtpSubmit}>
                                <Modal.Header>
                                    <Modal.Heading>Two-factor authentication</Modal.Heading>
                                </Modal.Header>
                                <Modal.Body className='space-y-4'>
                                    <p className='text-sm text-muted'>
                                        Enter the 6-digit code from your authenticator app.
                                    </p>
                                    {otpError && (
                                        <div
                                            className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'
                                            role='alert'
                                        >
                                            {otpError}
                                        </div>
                                    )}
                                    <div className='flex justify-center'>
                                        <InputOTP
                                            autoFocus
                                            maxLength={6}
                                            value={code}
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
                                </Modal.Body>
                                <Modal.Footer>
                                    <Button
                                        isDisabled={loading}
                                        type='button'
                                        variant='ghost'
                                        onPress={otpModal.close}
                                    >
                                        Cancel
                                    </Button>
                                    <Button
                                        isDisabled={loading || code.length !== 6}
                                        type='submit'
                                        variant='primary'
                                    >
                                        {loading ? (
                                            <span className='flex items-center justify-center gap-2'>
                                                <Loader2 className='h-4 w-4 animate-spin' />
                                                Verifying…
                                            </span>
                                        ) : (
                                            'Verify'
                                        )}
                                    </Button>
                                </Modal.Footer>
                            </form>
                        </Modal.Dialog>
                    </Modal.Container>
                </Modal.Backdrop>
            </Modal>
        </div>
    );
}
