import type { PurgeType } from '@/api';

import { Button, TextArea } from '@heroui/react';
import { Eraser, Globe2, Link2, Tags, Trash2 } from 'lucide-react';
import { useMemo, useState } from 'react';

import { ApiError, purgeApi } from '@/api';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormField } from '@/components/FormField.tsx';
import { useCluster } from '@/hooks/useCluster.ts';
import { canOperateCluster } from '@/utils/rbac.ts';

const customPurgeOptions: Array<{
    id: PurgeType;
    label: string;
    description: string;
    placeholder: string;
    inputLabel: string;
    validate: (value: string) => boolean;
    icon: typeof Link2;
}> = [
    {
        id: 'URL',
        label: 'Exact URL',
        description: 'Remove cached URLs without affecting related paths.',
        placeholder: 'https://example.com/assets/app.css',
        inputLabel: 'URLs to purge',
        validate: (value) => /^https?:\/\/\S+$/.test(value),
        icon: Link2,
    },
    {
        id: 'PREFIX',
        label: 'Path prefix',
        description: 'Remove every cached response below a path.',
        placeholder: '/assets/',
        inputLabel: 'Path prefixes',
        validate: (value) => value.startsWith('/'),
        icon: Globe2,
    },
    {
        id: 'TAG',
        label: 'Cache tag',
        description: 'Remove responses associated with surrogate keys.',
        placeholder: 'product:1234',
        inputLabel: 'Cache tags',
        validate: (value) => /^\S+$/.test(value),
        icon: Tags,
    },
];

const maxBatchValues = 100;

export function SiteCachePurge({
    site,
}: {
    site: { id: string; name: string; domains: string[] };
}) {
    const { clusterId, clusters } = useCluster();
    const canOperate = canOperateCluster(
        clusters.find((cluster) => cluster.id === clusterId)?.role
    );
    const api = useMemo(() => purgeApi(clusterId), [clusterId]);

    const [purgeAllOpen, setPurgeAllOpen] = useState(false);
    const [customOpen, setCustomOpen] = useState(false);
    const [type, setType] = useState<PurgeType>('URL');
    const [valuesText, setValuesText] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState('');
    const [message, setMessage] = useState('');

    const selectedPurge =
        customPurgeOptions.find((option) => option.id === type) ?? customPurgeOptions[0];
    const values = Array.from(
        new Set(
            valuesText
                .split(/\r?\n/)
                .map((item) => item.trim())
                .filter(Boolean)
        )
    );
    const invalidValues = values.filter((value) => !selectedPurge.validate(value));
    const batchValid =
        values.length > 0 && values.length <= maxBatchValues && invalidValues.length === 0;

    const purgeAll = async () => {
        setPurgeAllOpen(false);
        setSubmitting(true);
        setError('');
        setMessage('');
        try {
            await api.enqueue(site.id, { type: 'ALL' });
            setMessage('Full cache purge was queued.');
        } catch (purgeError) {
            setError(
                purgeError instanceof ApiError
                    ? purgeError.message
                    : 'Failed to enqueue cache purge'
            );
        } finally {
            setSubmitting(false);
        }
    };

    const purgeCustom = async () => {
        if (!batchValid) return;
        setCustomOpen(false);
        setSubmitting(true);
        setError('');
        setMessage('');
        try {
            let queued = 0;
            for (const value of values) {
                await api.enqueue(site.id, { type, value });
                queued += 1;
            }
            setMessage(
                `${queued} ${selectedPurge.label.toLowerCase()} purge job${queued === 1 ? '' : 's'} were queued.`
            );
            setValuesText('');
        } catch (purgeError) {
            setError(
                purgeError instanceof ApiError
                    ? purgeError.message
                    : 'Failed to enqueue cache purge'
            );
        } finally {
            setSubmitting(false);
        }
    };

    return (
        <>
            <ContentCard noPadding>
                <div className='flex flex-col gap-4 px-5 py-5 sm:flex-row sm:items-center sm:justify-between lg:px-6'>
                    <div className='flex items-center gap-3'>
                        <span className='flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground'>
                            <Eraser className='h-5 w-5' />
                        </span>
                        <div>
                            <h2 className='text-base font-semibold'>Purge cache</h2>
                            <p className='mt-0.5 text-xs text-muted'>
                                Invalidate cached responses for {site.name} without changing the
                                cache policy.
                            </p>
                        </div>
                    </div>
                    <div className='flex shrink-0 gap-2'>
                        <Button
                            isDisabled={!canOperate || submitting}
                            variant='secondary'
                            onPress={() => setCustomOpen(true)}
                        >
                            <Eraser className='mr-1.5 h-4 w-4' />
                            Custom purge
                        </Button>
                        <Button
                            isDisabled={!canOperate || submitting}
                            variant='danger'
                            onPress={() => setPurgeAllOpen(true)}
                        >
                            <Trash2 className='mr-1.5 h-4 w-4' />
                            Purge everything
                        </Button>
                    </div>
                </div>
                {(error || message) && (
                    <div className='border-t border-border px-5 py-4 lg:px-6'>
                        {error && (
                            <div className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger'>
                                {error}
                            </div>
                        )}
                        {message && (
                            <div className='rounded-lg border border-success/20 bg-success/10 px-4 py-3 text-sm text-success'>
                                {message}
                            </div>
                        )}
                    </div>
                )}
            </ContentCard>

            <ConfirmDialog
                danger
                confirmLabel='Purge everything'
                description={`All cached responses for ${site.name} will be removed. New requests will repopulate content from the origin.`}
                isOpen={purgeAllOpen}
                loading={submitting}
                title='Purge entire cache?'
                onConfirm={() => void purgeAll()}
                onOpenChange={setPurgeAllOpen}
            />

            <DialogShell
                icon={<Eraser className='h-4 w-4' />}
                isOpen={customOpen}
                size='lg'
                subtitle='Choose the smallest scope that contains the content you need to invalidate.'
                title='Custom purge'
                onOpenChange={setCustomOpen}
            >
                <div className='max-h-[65vh] space-y-5 overflow-y-auto px-6 py-5'>
                    <div className='grid gap-3'>
                        {customPurgeOptions.map((option) => {
                            const Icon = option.icon;
                            const selected = type === option.id;
                            return (
                                <button
                                    aria-pressed={selected}
                                    className={`flex items-start gap-3 rounded-xl border px-4 py-3.5 text-left transition-all ${
                                        selected
                                            ? 'border-primary bg-primary/10 shadow-sm ring-2 ring-primary/40'
                                            : 'border-border/70 hover:border-border hover:bg-surface-secondary/50'
                                    }`}
                                    key={option.id}
                                    type='button'
                                    onClick={() => setType(option.id)}
                                >
                                    <span
                                        className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ${
                                            selected
                                                ? 'bg-primary text-primary-foreground'
                                                : 'bg-surface-secondary text-muted'
                                        }`}
                                    >
                                        <Icon className='h-4 w-4' />
                                    </span>
                                    <span>
                                        <span className='block text-sm font-medium'>
                                            {option.label}
                                        </span>
                                        <span className='mt-0.5 block text-xs leading-5 text-muted'>
                                            {option.description}
                                        </span>
                                    </span>
                                </button>
                            );
                        })}
                    </div>

                    <FormField
                        error={
                            invalidValues.length > 0
                                ? `${invalidValues.length} value${invalidValues.length === 1 ? '' : 's'} do not match the ${selectedPurge.label.toLowerCase()} format.`
                                : values.length > maxBatchValues
                                  ? `Keep at most ${maxBatchValues} values.`
                                  : undefined
                        }
                        hint={`One value per line. Duplicates are removed. ${values.length}/${maxBatchValues} values.`}
                        htmlFor='custom-purge-values'
                        label={selectedPurge.inputLabel}
                    >
                        <TextArea
                            className='w-full'
                            id='custom-purge-values'
                            placeholder={`${selectedPurge.placeholder}\n${selectedPurge.placeholder}`}
                            rows={5}
                            value={valuesText}
                            variant='secondary'
                            onChange={(event) => setValuesText(event.target.value)}
                        />
                    </FormField>
                </div>
                <DialogFooter>
                    <Button variant='ghost' onPress={() => setCustomOpen(false)}>
                        Cancel
                    </Button>
                    <Button
                        isDisabled={!batchValid || submitting}
                        onPress={() => void purgeCustom()}
                    >
                        <Eraser className='mr-1.5 h-4 w-4' />
                        {submitting ? 'Queuing...' : `Purge ${values.length || ''}`.trim()}
                    </Button>
                </DialogFooter>
            </DialogShell>
        </>
    );
}
