import type { ReactNode } from 'react';

import { AlertDialog, Button } from '@heroui/react';

interface ConfirmDialogProps {
    isOpen: boolean;
    onOpenChange: (open: boolean) => void;
    title: string;
    description?: ReactNode;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    loading?: boolean;
    onConfirm: () => void;
}

export function ConfirmDialog({
    isOpen,
    onOpenChange,
    title,
    description,
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    danger = false,
    loading = false,
    onConfirm,
}: ConfirmDialogProps) {
    return (
        <AlertDialog isOpen={isOpen} onOpenChange={onOpenChange}>
            <AlertDialog.Backdrop>
                <AlertDialog.Container placement='center'>
                    <AlertDialog.Dialog>
                        <AlertDialog.CloseTrigger />
                        <AlertDialog.Header>
                            <AlertDialog.Icon status={danger ? 'danger' : 'warning'} />
                            <AlertDialog.Heading>{title}</AlertDialog.Heading>
                        </AlertDialog.Header>
                        {description && <AlertDialog.Body>{description}</AlertDialog.Body>}
                        <AlertDialog.Footer>
                            <Button
                                isDisabled={loading}
                                variant='ghost'
                                onPress={() => onOpenChange(false)}
                            >
                                {cancelLabel}
                            </Button>
                            <Button
                                isDisabled={loading}
                                variant={danger ? 'danger' : 'primary'}
                                onPress={onConfirm}
                            >
                                {loading ? 'Working...' : confirmLabel}
                            </Button>
                        </AlertDialog.Footer>
                    </AlertDialog.Dialog>
                </AlertDialog.Container>
            </AlertDialog.Backdrop>
        </AlertDialog>
    );
}
