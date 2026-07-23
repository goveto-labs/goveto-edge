import type { SSHCredential } from '@/api';

import { Button } from '@heroui/react';
import { Plus } from 'lucide-react';

interface SSHCredentialSelectProps {
    credentials: SSHCredential[];
    id: string;
    value: string;
    onChange: (credentialId: string) => void;
    onAdd?: () => void;
}

function authTypeLabel(credential: SSHCredential) {
    return credential.auth_type === 'PASSWORD' ? 'Password' : 'Private key';
}

export function SSHCredentialSelect({
    credentials,
    id,
    value,
    onChange,
    onAdd,
}: SSHCredentialSelectProps) {
    return (
        <div className='flex flex-col gap-2 sm:flex-row sm:items-center'>
            <select
                aria-label='SSH credential'
                className='h-10 min-w-0 flex-1 rounded-xl border border-border bg-surface-secondary px-3 text-sm outline-none focus:border-accent md:h-9'
                disabled={credentials.length === 0}
                id={id}
                required
                value={value}
                onChange={(event) => onChange(event.target.value)}
            >
                <option value=''>
                    {credentials.length === 0 ? 'No credentials available' : 'Select a credential'}
                </option>
                {credentials.map((credential) => (
                    <option key={credential.id} value={credential.id}>
                        {credential.name} · {credential.username} · {authTypeLabel(credential)}
                    </option>
                ))}
            </select>

            {onAdd && (
                <Button
                    className='w-full shrink-0 sm:w-auto'
                    type='button'
                    variant='secondary'
                    onPress={onAdd}
                >
                    <Plus className='mr-1.5 h-4 w-4' />
                    Add credential
                </Button>
            )}
        </div>
    );
}
