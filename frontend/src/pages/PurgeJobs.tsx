import type { PurgeJob, PurgeType } from '@/api';

import { Button, Card, Input, Label, ListBox, Select, Spinner, Table } from '@heroui/react';
import { Eraser, Search } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import { ApiError, purgeApi } from '@/api';
import { PageHeader } from '@/components/PageHeader.tsx';
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
                <Card className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to manage purge jobs.
                    </div>
                </Card>
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

            <Card className='p-5'>
                <form className='space-y-4' onSubmit={handleSubmit}>
                    <div className='flex flex-col gap-4 md:flex-row md:items-end'>
                        <div className='flex flex-col gap-1 flex-1'>
                            <Label htmlFor='purge-site-id'>Site ID</Label>
                            <Input
                                variant='secondary'
                                id='purge-site-id'
                                placeholder='Site ID'
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
                            variant='secondary'
                            className='flex-1'
                            aria-label='Purge value'
                            disabled={type === 'ALL'}
                            placeholder={type === 'ALL' ? 'No value needed' : 'Value to purge'}
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
            </Card>

            <Card className='overflow-hidden'>
                <Table>
                    <Table.ScrollContainer>
                        <Table.Content aria-label='Purge jobs'>
                            <Table.Header>
                                <Table.Column isRowHeader>Job ID</Table.Column>
                                <Table.Column>Type</Table.Column>
                                <Table.Column>Value</Table.Column>
                                <Table.Column>Status</Table.Column>
                                <Table.Column>Created</Table.Column>
                            </Table.Header>
                            <Table.Body>
                                {loading && jobs.length === 0 && (
                                    <Table.Row id='loading'>
                                        <Table.Cell>
                                            <div className='flex justify-center py-4'>
                                                <Spinner />
                                            </div>
                                        </Table.Cell>
                                        <Table.Cell />
                                        <Table.Cell />
                                        <Table.Cell />
                                        <Table.Cell />
                                    </Table.Row>
                                )}
                                {jobs.map((job) => (
                                    <Table.Row key={job.id} id={job.id}>
                                        <Table.Cell className='font-mono text-xs'>
                                            {job.id}
                                        </Table.Cell>
                                        <Table.Cell>{job.type}</Table.Cell>
                                        <Table.Cell>{job.value ?? '-'}</Table.Cell>
                                        <Table.Cell>{job.status}</Table.Cell>
                                        <Table.Cell>{job.created_at}</Table.Cell>
                                    </Table.Row>
                                ))}
                            </Table.Body>
                        </Table.Content>
                    </Table.ScrollContainer>
                </Table>
            </Card>
        </div>
    );
}
