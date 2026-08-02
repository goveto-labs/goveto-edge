import type { ReactNode } from 'react';

import { Button } from '@heroui/react';
import { AlertTriangle, Undo2 } from 'lucide-react';

interface SettingsActionBarProps {
    children: ReactNode;
    error?: ReactNode;
    isDirty: boolean;
    isDiscardDisabled?: boolean;
    onDiscard: () => void;
}

export function SettingsActionBar({
    children,
    error,
    isDirty,
    isDiscardDisabled = false,
    onDiscard,
}: SettingsActionBarProps) {
    if (!isDirty) return null;

    return (
        <div className='sticky bottom-20 z-20 mx-0 mt-3 flex flex-col gap-3 rounded-xl border border-warning/40 bg-surface/95 px-4 py-3 shadow-xl backdrop-blur supports-[backdrop-filter]:bg-surface/90 sm:mx-8 sm:flex-row sm:items-center sm:justify-between md:bottom-4'>
            <div aria-live='polite' className='flex min-w-0 flex-col items-start gap-1.5'>
                <div className='inline-flex w-fit items-center gap-2 rounded-lg bg-warning/15 px-2.5 py-1.5 text-xs font-semibold text-warning'>
                    <AlertTriangle className='h-4 w-4 shrink-0' />
                    Unsaved changes
                </div>
                {error && <div className='text-xs text-danger'>{error}</div>}
            </div>
            <div className='grid w-full shrink-0 grid-cols-1 gap-2 sm:flex sm:w-auto sm:items-center sm:justify-end'>
                <Button
                    isDisabled={isDiscardDisabled}
                    type='button'
                    variant='secondary'
                    onPress={onDiscard}
                >
                    <Undo2 className='h-4 w-4' />
                    Discard
                </Button>
                {children}
            </div>
        </div>
    );
}
