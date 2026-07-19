import type { ReactNode } from 'react';

import { Button, Modal } from '@heroui/react';
import { X } from 'lucide-react';

interface DialogShellProps {
    children: ReactNode;
    isOpen: boolean;
    onOpenChange: (open: boolean) => void;
    title: ReactNode;
    subtitle?: ReactNode;
    icon?: ReactNode;
    size?: 'sm' | 'md' | 'lg';
    isDismissable?: boolean;
}

export function DialogShell({
    children,
    isOpen,
    onOpenChange,
    title,
    subtitle,
    icon,
    size = 'md',
    isDismissable = true,
}: DialogShellProps) {
    return (
        <Modal isOpen={isOpen} onOpenChange={onOpenChange}>
            <Modal.Backdrop className='dialog-shell-backdrop' isDismissable={isDismissable}>
                <Modal.Container className='dialog-shell-container' placement='center' size={size}>
                    <Modal.Dialog className='overflow-hidden rounded-2xl border border-border bg-surface p-0 shadow-xl'>
                        <div className='flex items-start justify-between border-b border-border bg-surface-secondary/50 px-6 py-4'>
                            <div className='flex items-center gap-3'>
                                {icon && (
                                    <div className='flex h-9 w-9 items-center justify-center rounded-lg bg-surface text-muted'>
                                        {icon}
                                    </div>
                                )}
                                <div>
                                    <Modal.Heading className='text-lg font-semibold'>
                                        {title}
                                    </Modal.Heading>
                                    {subtitle && <p className='text-sm text-muted'>{subtitle}</p>}
                                </div>
                            </div>
                            <Button
                                isIconOnly
                                className='-mr-2 -mt-2 text-muted'
                                size='sm'
                                variant='ghost'
                                onPress={() => onOpenChange(false)}
                            >
                                <X className='h-4 w-4' />
                            </Button>
                        </div>
                        {children}
                    </Modal.Dialog>
                </Modal.Container>
            </Modal.Backdrop>
        </Modal>
    );
}

interface DialogFooterProps {
    children: ReactNode;
}

export function DialogFooter({ children }: DialogFooterProps) {
    return (
        <div className='flex justify-end gap-2 border-t border-border bg-surface-secondary/30 px-6 py-4'>
            {children}
        </div>
    );
}
