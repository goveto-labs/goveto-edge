import type { SSHCredential } from '@/api';

import { Button } from '@heroui/react';
import { Plus } from 'lucide-react';

import { SelectField } from '@/components/SelectField.tsx';

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
            <SelectField
                ariaLabel='SSH credential'
                className='min-w-0 flex-1'
                id={id}
                isDisabled={credentials.length === 0}
                isRequired
                options={credentials.map((credential) => ({
                    id: credential.id,
                    label: `${credential.name} · ${credential.username} · ${authTypeLabel(credential)}`,
                }))}
                placeholder={
                    credentials.length === 0 ? 'No credentials available' : 'Select a credential'
                }
                value={value}
                variant='secondary'
                onChange={onChange}
            />

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
