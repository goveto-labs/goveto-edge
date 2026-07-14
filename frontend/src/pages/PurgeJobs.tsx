import type { PurgeJob, PurgeType } from '@/api';

import { Button, Input, Label, ListBox, Select, Spinner } from '@heroui/react';
import { Eraser, Search } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import { ApiError, purgeApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

const purgeTypes: PurgeType[] = ['URL', 'PREFIX', 'TAG', 'ALL'];

export default function PurgeJobs() {
    const { clusterId } = useCluster();
    const api = useMemo(() => purgeApi(clusterId), [clusterId]);
    const [searchParams, setSearchParams] = useSearchParams();
    const siteId = searchParams.get('siteId') ?? '';

    const [jobs, setJobs] = useState<PurgeJob[]>([]);
    const [type, setType] = useState<PurgeType>('URL');
    const [value, setValue] = useState('');
    const [loading, setLoading] = useState(false);
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState('');

    const load = useCallback(async () => {
        if (!clusterId || !siteId.trim()) return;
        setLoading(true);
        try {
            const data = await api.list(siteId.trim());
            setJobs(data);
            setError('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load purge jobs');
        } finally {
            setLoading(false);
        }
    }, [api, clusterId, siteId]);

    useEffect(() => {
        load();
    }, [load]);

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!siteId.trim()) return;
        setSubmitting(true);
        setError('');
        try {
            const job = await api.enqueue(siteId.trim(), {
                type,
                value: type === 'ALL' ? undefined : value.trim(),
            });
            setJobs((prev) => [job, ...prev]);
            setValue('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to enqueue purge');
        } finally {
            setSubmitting(false);
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Clear cached content by URL, prefix, tag, or site.'
                    title='Purge Jobs'
                />
                <ContentCard className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to manage purge jobs.
                    </div>
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader
                subtitle='Clear cached content by URL, prefix, tag, or site.'
                title='Purge Jobs'
            />

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <ContentCard title='Enqueue purge'>
                <form className='space-y-4' onSubmit={handleSubmit}>
                    <div className='flex flex-col gap-4 md:flex-row md:items-end'>
                        <div className='flex flex-1 flex-col gap-1'>
                            <Label htmlFor='purge-site-id'>Site ID</Label>
                            <Input
                                id='purge-site-id'
                                placeholder='Site ID'
                                variant='secondary'
                                value={siteId}
                                onChange={(e) => {
                                    const next = e.target.value;
                                    if (!next) {
                                        setSearchParams({});
                                    } else {
                                        setSearchParams({ siteId: next });
                                    }
                                }}
                            />
                        </div>
                        <Button className='w-full md:w-auto' variant='ghost' onPress={load}>
                            <Search className='mr-2 h-4 w-4' />
                            {loading ? 'Loading...' : 'Load'}
                        </Button>
                    </div>

                    <div className='flex flex-col gap-3 sm:flex-row sm:items-end'>
                        <Select
                            className='w-full sm:w-40'
                            variant='secondary'
                            value={type}
                            onChange={(key) => setType(String(key ?? '') as PurgeType)}
                        >
                            <Label>Type</Label>
                            <Select.Trigger>
                                <Select.Value />
                            </Select.Trigger>
                            <Select.Popover>
                                <ListBox>
                                    {purgeTypes.map((t) => (
                                        <ListBox.Item key={t} id={t} textValue={t}>
                                            {t}
                                        </ListBox.Item>
                                    ))}
                                </ListBox>
                            </Select.Popover>
                        </Select>
                        <Input
                            aria-label='Purge value'
                            className='flex-1'
                            disabled={type === 'ALL'}
                            placeholder={type === 'ALL' ? 'No value needed' : 'Value to purge'}
                            variant='secondary'
                            value={value}
                            onChange={(e) => setValue(e.target.value)}
                        />
                        <Button
                            className='w-full sm:w-auto'
                            isDisabled={submitting}
                            type='submit'
                            variant='primary'
                        >
                            <Eraser className='mr-2 h-4 w-4' />
                            {submitting ? 'Purging...' : 'Purge'}
                        </Button>
                    </div>
                </form>
            </ContentCard>

            <DataTable aria-label='Purge jobs'>
                <thead>
                    <tr className='border-b border-border'>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Job ID</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Type</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Value</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Status</th>
                        <th className='py-3 text-left text-xs font-medium text-muted'>Created</th>
                    </tr>
                </thead>
                <tbody>
                    {loading && jobs.length === 0 && (
                        <tr id='loading'>
                            <td colSpan={5}>
                                <div className='flex justify-center py-4'>
                                    <Spinner />
                                </div>
                            </td>
                        </tr>
                    )}
                    {jobs.map((job) => (
                        <tr className='border-b border-border last:border-0' key={job.id}>
                            <td className='py-3 font-mono text-xs'>{job.id}</td>
                            <td className='py-3 text-sm'>{job.type}</td>
                            <td className='py-3 text-sm text-muted'>{job.value ?? '-'}</td>
                            <td className='py-3'>
                                <StatusBadge status={job.status} />
                            </td>
                            <td className='py-3 text-sm text-muted'>{job.created_at}</td>
                        </tr>
                    ))}
                </tbody>
            </DataTable>
        </div>
    );
}
