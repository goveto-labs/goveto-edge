import type { ReactNode } from 'react';

import { AlertDialog, Button, Input } from '@heroui/react';
import { useEffect, useId, useState } from 'react';

import { FormError } from '@/components/FormField.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

interface ConfirmDialogProps {
    isOpen: boolean;
    onOpenChange: (open: boolean) => void;
    title: string;
    description?: ReactNode;
    confirmLabel: string;
    cancelLabel?: string;
    danger?: boolean;
    loading?: boolean;
    clusterName?: string;
    impact?: ReactNode;
    recoverability?: string;
    confirmationText?: string;
    error?: string;
    onConfirm: () => void;
}

export function ConfirmDialog({
    isOpen,
    onOpenChange,
    title,
    description,
    confirmLabel,
    cancelLabel = 'Cancel',
    danger = false,
    loading = false,
    clusterName,
    impact,
    recoverability,
    confirmationText,
    error,
    onConfirm,
}: ConfirmDialogProps) {
    const { clusterId, clusters } = useCluster();
    const confirmationInputId = useId();
    const [typedConfirmation, setTypedConfirmation] = useState('');
    const matchesConfirmation = !confirmationText || typedConfirmation === confirmationText;
    const hasSpecificActionLabel =
        Boolean(confirmLabel.trim()) && confirmLabel.trim().toLowerCase() !== 'confirm';
    const resolvedClusterName =
        clusterName || clusters.find((cluster) => cluster.id === clusterId)?.name;

    // biome-ignore lint/correctness/useExhaustiveDependencies: changing the open target must clear prior confirmation input
    useEffect(() => setTypedConfirmation(''), [confirmationText, isOpen]);

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
                        <AlertDialog.Body>
                            <div className='space-y-3'>
                                {description && <div>{description}</div>}
                                {(resolvedClusterName || impact || recoverability) && (
                                    <dl className='grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 rounded-lg bg-surface-secondary p-3 text-sm'>
                                        {resolvedClusterName && (
                                            <>
                                                <dt className='text-muted'>Cluster</dt>
                                                <dd className='font-medium'>
                                                    {resolvedClusterName}
                                                </dd>
                                            </>
                                        )}
                                        {impact && (
                                            <>
                                                <dt className='text-muted'>Impact</dt>
                                                <dd>{impact}</dd>
                                            </>
                                        )}
                                        {recoverability && (
                                            <>
                                                <dt className='text-muted'>Recovery</dt>
                                                <dd>{recoverability}</dd>
                                            </>
                                        )}
                                    </dl>
                                )}
                                {confirmationText && (
                                    <label
                                        className='block text-sm font-medium'
                                        htmlFor={confirmationInputId}
                                    >
                                        Type <span className='font-mono'>{confirmationText}</span>{' '}
                                        to continue
                                        <Input
                                            aria-label={`Type ${confirmationText} to confirm`}
                                            autoComplete='off'
                                            className='mt-2'
                                            id={confirmationInputId}
                                            value={typedConfirmation}
                                            variant='secondary'
                                            onChange={(event) =>
                                                setTypedConfirmation(event.target.value)
                                            }
                                        />
                                    </label>
                                )}
                                {error && <FormError message={error} />}
                            </div>
                        </AlertDialog.Body>
                        <AlertDialog.Footer>
                            <Button
                                isDisabled={loading}
                                variant='ghost'
                                onPress={() => onOpenChange(false)}
                            >
                                {cancelLabel}
                            </Button>
                            <Button
                                isDisabled={
                                    loading || !matchesConfirmation || !hasSpecificActionLabel
                                }
                                variant={danger ? 'danger' : 'primary'}
                                onPress={onConfirm}
                            >
                                {loading
                                    ? 'Working…'
                                    : hasSpecificActionLabel
                                      ? confirmLabel
                                      : 'Action unavailable'}
                            </Button>
                        </AlertDialog.Footer>
                    </AlertDialog.Dialog>
                </AlertDialog.Container>
            </AlertDialog.Backdrop>
        </AlertDialog>
    );
}
