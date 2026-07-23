import type { SSHAuthType, SSHCredential, SSHCredentialWriteRequest } from '@/api/types.ts';

import { Button, Input, TextArea } from '@heroui/react';
import { KeyRound, LockKeyhole } from 'lucide-react';
import { useEffect, useState } from 'react';

import { ApiError } from '@/api';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';

interface SSHCredentialDialogProps {
    credential?: SSHCredential | null;
    isOpen: boolean;
    onOpenChange: (open: boolean) => void;
    onSave: (payload: SSHCredentialWriteRequest) => Promise<SSHCredential>;
    onSaved?: (credential: SSHCredential) => void;
}

export function SSHCredentialDialog({
    credential,
    isOpen,
    onOpenChange,
    onSave,
    onSaved,
}: SSHCredentialDialogProps) {
    const editing = Boolean(credential);
    const [name, setName] = useState('');
    const [username, setUsername] = useState('root');
    const [authType, setAuthType] = useState<SSHAuthType>('PASSWORD');
    const [password, setPassword] = useState('');
    const [privateKey, setPrivateKey] = useState('');
    const [passphrase, setPassphrase] = useState('');
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState('');

    useEffect(() => {
        if (!isOpen) return;
        setName(credential?.name ?? '');
        setUsername(credential?.username ?? 'root');
        setAuthType(credential?.auth_type ?? 'PASSWORD');
        setPassword('');
        setPrivateKey('');
        setPassphrase('');
        setError('');
    }, [credential, isOpen]);

    const submit = async (event: React.FormEvent) => {
        event.preventDefault();
        setSaving(true);
        setError('');
        try {
            const payload: SSHCredentialWriteRequest = {
                name: name.trim(),
                username: username.trim(),
                auth_type: authType,
            };
            if (authType === 'PASSWORD') {
                if (password) payload.password = password;
            } else {
                if (privateKey) payload.private_key = privateKey;
                if (passphrase) payload.passphrase = passphrase;
            }
            const saved = await onSave(payload);
            onSaved?.(saved);
            onOpenChange(false);
        } catch (saveError) {
            setError(
                saveError instanceof ApiError ? saveError.message : 'Failed to save SSH credential'
            );
        } finally {
            setSaving(false);
        }
    };

    const requiresSecret = !editing || authType !== credential?.auth_type;
    const secretMissing = requiresSecret && (authType === 'PASSWORD' ? !password : !privateKey);

    return (
        <DialogShell
            icon={<KeyRound className='h-4 w-4' />}
            isDismissable={!saving}
            isOpen={isOpen}
            size='lg'
            subtitle='Secrets are encrypted before they are stored in PostgreSQL.'
            title={editing ? 'Edit SSH credential' : 'Add SSH credential'}
            onOpenChange={onOpenChange}
        >
            <form onSubmit={submit}>
                <div className='space-y-5 px-6 py-5'>
                    {error && <FormError message={error} />}
                    <div className='grid gap-4 sm:grid-cols-2'>
                        <FormField label='Credential name' required>
                            <Input
                                required
                                autoFocus
                                value={name}
                                variant='secondary'
                                onChange={(event) => setName(event.target.value)}
                            />
                        </FormField>
                        <FormField label='SSH user' required>
                            <Input
                                required
                                value={username}
                                variant='secondary'
                                onChange={(event) => setUsername(event.target.value)}
                            />
                        </FormField>
                    </div>

                    <FormField label='Authentication method' required>
                        <div className='grid gap-3 sm:grid-cols-2'>
                            <button
                                aria-pressed={authType === 'PASSWORD'}
                                className={`flex items-start gap-3 rounded-xl border p-4 text-left transition-colors ${
                                    authType === 'PASSWORD'
                                        ? 'border-accent bg-accent/10'
                                        : 'border-border bg-surface hover:bg-surface-secondary'
                                }`}
                                type='button'
                                onClick={() => {
                                    setAuthType('PASSWORD');
                                    setPrivateKey('');
                                    setPassphrase('');
                                }}
                            >
                                <LockKeyhole className='mt-0.5 h-5 w-5 shrink-0 text-muted' />
                                <span>
                                    <span className='block text-sm font-semibold'>Password</span>
                                    <span className='mt-1 block text-xs leading-5 text-muted'>
                                        Store the SSH account password.
                                    </span>
                                </span>
                            </button>
                            <button
                                aria-pressed={authType === 'PRIVATE_KEY'}
                                className={`flex items-start gap-3 rounded-xl border p-4 text-left transition-colors ${
                                    authType === 'PRIVATE_KEY'
                                        ? 'border-accent bg-accent/10'
                                        : 'border-border bg-surface hover:bg-surface-secondary'
                                }`}
                                type='button'
                                onClick={() => {
                                    setAuthType('PRIVATE_KEY');
                                    setPassword('');
                                }}
                            >
                                <KeyRound className='mt-0.5 h-5 w-5 shrink-0 text-muted' />
                                <span>
                                    <span className='block text-sm font-semibold'>Private key</span>
                                    <span className='mt-1 block text-xs leading-5 text-muted'>
                                        Store a PEM-encoded private key and optional passphrase.
                                    </span>
                                </span>
                            </button>
                        </div>
                    </FormField>

                    {authType === 'PASSWORD' ? (
                        <FormField
                            hint={editing ? 'Leave blank to keep the current password.' : undefined}
                            label='Password'
                            required={requiresSecret}
                        >
                            <Input
                                required={requiresSecret}
                                type='password'
                                value={password}
                                variant='secondary'
                                onChange={(event) => setPassword(event.target.value)}
                            />
                        </FormField>
                    ) : (
                        <>
                            <FormField
                                hint={
                                    editing
                                        ? 'Leave blank to keep the current private key.'
                                        : 'Include the complete BEGIN and END lines.'
                                }
                                label='Private key PEM'
                                required={requiresSecret}
                            >
                                <TextArea
                                    required={requiresSecret}
                                    className='font-mono text-xs'
                                    rows={8}
                                    spellCheck={false}
                                    value={privateKey}
                                    variant='secondary'
                                    onChange={(event) => setPrivateKey(event.target.value)}
                                />
                            </FormField>
                            <FormField
                                hint={
                                    editing
                                        ? 'Leave blank to keep the current passphrase.'
                                        : undefined
                                }
                                label='Private key passphrase'
                            >
                                <Input
                                    type='password'
                                    value={passphrase}
                                    variant='secondary'
                                    onChange={(event) => setPassphrase(event.target.value)}
                                />
                            </FormField>
                        </>
                    )}
                </div>
                <DialogFooter>
                    <Button type='button' variant='ghost' onPress={() => onOpenChange(false)}>
                        Cancel
                    </Button>
                    <Button
                        isDisabled={saving || !name.trim() || !username.trim() || secretMissing}
                        type='submit'
                        variant='primary'
                    >
                        {saving ? 'Saving…' : editing ? 'Save changes' : 'Add credential'}
                    </Button>
                </DialogFooter>
            </form>
        </DialogShell>
    );
}
