import type { Certificate, SiteOrigin } from '@/api';

import { Button, Input, ListBox, Select } from '@heroui/react';
import { ArrowLeft, Globe2, Plus, Server, ShieldCheck, Trash2 } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, certificatesApi, sitesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DomainAddField } from '@/components/DomainAddField.tsx';
import { FormError } from '@/components/FormField.tsx';
import { FormRow } from '@/components/FormRow.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SearchableMultiAddField } from '@/components/SearchableMultiAddField.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

type OriginFormItem = SiteOrigin & { localId: string };

function createOrigin(): OriginFormItem {
    return {
        localId: crypto.randomUUID(),
        protocol: 'HTTP',
        address: '',
        host_header: '',
        weight: 1,
    };
}

function StepNavigation() {
    return (
        <div className='flex flex-col gap-1'>
            <a
                className='flex items-center gap-2 rounded-lg bg-surface-secondary px-3 py-2 text-sm font-medium text-foreground'
                href='#site-information'
            >
                <Globe2 className='h-4 w-4' />
                Site information
            </a>
            <a
                className='flex items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-muted transition-colors hover:bg-surface-secondary hover:text-foreground'
                href='#site-origins'
            >
                <Server className='h-4 w-4' />
                Origins
            </a>
        </div>
    );
}

function SectionHeader({ number, title }: { number: number; title: string }) {
    return (
        <div className='flex items-center gap-3 border-b border-border bg-surface-secondary/30 px-6 py-3'>
            <span className='flex h-6 w-6 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground'>
                {number}
            </span>
            <span className='text-sm font-semibold'>{title}</span>
        </div>
    );
}

export default function CreateSite() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const api = useMemo(() => sitesApi(clusterId), [clusterId]);
    const certApi = useMemo(() => certificatesApi(clusterId), [clusterId]);
    const [certificates, setCertificates] = useState<Certificate[]>([]);
    const [certificateIds, setCertificateIds] = useState<Set<string>>(new Set());
    const [name, setName] = useState('');
    const [domains, setDomains] = useState<string[]>([]);
    const [origins, setOrigins] = useState<OriginFormItem[]>([createOrigin()]);
    const [loadingOptions, setLoadingOptions] = useState(false);
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState('');

    useEffect(() => {
        if (!clusterId) return;
        setLoadingOptions(true);
        certApi
            .list()
            .then(setCertificates)
            .catch((loadError) =>
                setError(
                    loadError instanceof ApiError
                        ? loadError.message
                        : 'Failed to load certificates'
                )
            )
            .finally(() => setLoadingOptions(false));
    }, [certApi, clusterId]);

    const certificateOptions = useMemo(
        () =>
            certificates.map((certificate) => ({
                id: certificate.id,
                name: certificate.name,
                detail: certificate.expires_at
                    ? `Expires ${new Date(certificate.expires_at).toLocaleDateString()}`
                    : undefined,
            })),
        [certificates]
    );

    const updateOrigin = (localId: string, patch: Partial<SiteOrigin>) => {
        setOrigins((current) =>
            current.map((origin) => (origin.localId === localId ? { ...origin, ...patch } : origin))
        );
    };

    const handleSubmit = async (event: React.FormEvent) => {
        event.preventDefault();
        const validOrigins = origins.filter((origin) => origin.address.trim());
        if (domains.length === 0) {
            setError('Add at least one domain.');
            return;
        }
        if (validOrigins.length === 0) {
            setError('Add at least one origin address.');
            return;
        }

        setSubmitting(true);
        setError('');
        try {
            const created = await api.create({
                name: name.trim(),
                domains,
                certificate_ids: Array.from(certificateIds),
                origins: validOrigins.map(({ localId: _, ...origin }) => origin),
            });
            navigate(`/sites?siteId=${created.id}`);
        } catch (createError) {
            setError(
                createError instanceof ApiError ? createError.message : 'Failed to create site'
            );
        } finally {
            setSubmitting(false);
        }
    };

    if (!clusterId) return <FormError message='Select a cluster to create a site.' />;

    return (
        <div className='space-y-6'>
            <PageHeader
                actions={
                    <Button variant='ghost' onPress={() => navigate('/sites')}>
                        <ArrowLeft className='mr-1.5 h-4 w-4' />
                        Back to sites
                    </Button>
                }
                subtitle='Configure domains, certificates, and origin servers.'
                title='Create site'
            />

            <form onSubmit={handleSubmit}>
                <div className='grid grid-cols-1 gap-6 lg:grid-cols-[200px_1fr]'>
                    <div className='hidden lg:block'>
                        <StepNavigation />
                    </div>

                    <div className='space-y-6'>
                        {error && <FormError message={error} />}

                        <div className='scroll-mt-20' id='site-information'>
                            <ContentCard className='overflow-visible p-0' noPadding>
                                <SectionHeader number={1} title='Site information' />
                                <div className='px-6 py-2'>
                                    <FormRow htmlFor='site-name' label='Site name' required>
                                        <Input
                                            autoFocus
                                            id='site-name'
                                            required
                                            value={name}
                                            variant='secondary'
                                            onChange={(event) => setName(event.target.value)}
                                        />
                                    </FormRow>
                                    <FormRow
                                        hint='Add one hostname or paste multiple hostnames, one per line.'
                                        label='Domains'
                                        required
                                    >
                                        <DomainAddField value={domains} onChange={setDomains} />
                                    </FormRow>
                                    <FormRow
                                        hint='Optional. Select certificates when this site will serve HTTPS.'
                                        label='Certificates'
                                    >
                                        <SearchableMultiAddField
                                            addLabel='Add certificate'
                                            dialogTitle='Select certificates'
                                            emptyLabel={
                                                loadingOptions
                                                    ? 'Loading certificates…'
                                                    : 'No certificates selected'
                                            }
                                            itemLabel='certificate'
                                            options={certificateOptions}
                                            searchPlaceholder='Search certificates…'
                                            selected={certificateIds}
                                            onChange={setCertificateIds}
                                        />
                                    </FormRow>
                                </div>
                            </ContentCard>
                        </div>

                        <div className='scroll-mt-20' id='site-origins'>
                            <ContentCard className='overflow-visible p-0' noPadding>
                                <SectionHeader number={2} title='Origin servers' />
                                <div className='space-y-4 p-6'>
                                    <p className='text-sm text-muted'>
                                        Requests are forwarded to these backends. Add multiple
                                        origins for weighted load distribution.
                                    </p>
                                    {origins.map((origin, index) => (
                                        <div
                                            className='rounded-xl border border-border bg-surface-secondary/20 p-4'
                                            key={origin.localId}
                                        >
                                            <div className='mb-4 flex items-center justify-between gap-3'>
                                                <div className='text-sm font-semibold'>
                                                    Origin {index + 1}
                                                </div>
                                                {origins.length > 1 && (
                                                    <Button
                                                        isIconOnly
                                                        aria-label={`Remove origin ${index + 1}`}
                                                        size='sm'
                                                        type='button'
                                                        variant='ghost'
                                                        onPress={() =>
                                                            setOrigins((current) =>
                                                                current.filter(
                                                                    (item) =>
                                                                        item.localId !==
                                                                        origin.localId
                                                                )
                                                            )
                                                        }
                                                    >
                                                        <Trash2 className='h-4 w-4 text-muted' />
                                                    </Button>
                                                )}
                                            </div>
                                            <div className='grid gap-4 md:grid-cols-2'>
                                                <div className='flex gap-2'>
                                                    <Select
                                                        aria-label={`Origin ${index + 1} protocol`}
                                                        className='w-32 shrink-0'
                                                        value={origin.protocol}
                                                        variant='secondary'
                                                        onChange={(key) =>
                                                            updateOrigin(origin.localId, {
                                                                protocol: String(key) as
                                                                    | 'HTTP'
                                                                    | 'HTTPS',
                                                            })
                                                        }
                                                    >
                                                        <Select.Trigger>
                                                            <Select.Value />
                                                        </Select.Trigger>
                                                        <Select.Popover>
                                                            <ListBox>
                                                                <ListBox.Item
                                                                    id='HTTP'
                                                                    textValue='HTTP'
                                                                >
                                                                    HTTP
                                                                </ListBox.Item>
                                                                <ListBox.Item
                                                                    id='HTTPS'
                                                                    textValue='HTTPS'
                                                                >
                                                                    HTTPS
                                                                </ListBox.Item>
                                                            </ListBox>
                                                        </Select.Popover>
                                                    </Select>
                                                    <Input
                                                        aria-label={`Origin ${index + 1} address`}
                                                        className='flex-1'
                                                        placeholder='origin.example.com:80'
                                                        required={index === 0}
                                                        value={origin.address}
                                                        variant='secondary'
                                                        onChange={(event) =>
                                                            updateOrigin(origin.localId, {
                                                                address: event.target.value,
                                                            })
                                                        }
                                                    />
                                                </div>
                                                <Input
                                                    aria-label={`Origin ${index + 1} host header`}
                                                    placeholder='Host header (optional)'
                                                    value={origin.host_header}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        updateOrigin(origin.localId, {
                                                            host_header: event.target.value,
                                                        })
                                                    }
                                                />
                                                <Input
                                                    aria-label={`Origin ${index + 1} weight`}
                                                    className='md:max-w-48'
                                                    min={1}
                                                    placeholder='Weight'
                                                    type='number'
                                                    value={String(origin.weight ?? 1)}
                                                    variant='secondary'
                                                    onChange={(event) =>
                                                        updateOrigin(origin.localId, {
                                                            weight: Number(event.target.value),
                                                        })
                                                    }
                                                />
                                            </div>
                                        </div>
                                    ))}
                                    <Button
                                        className='w-fit'
                                        size='sm'
                                        type='button'
                                        variant='ghost'
                                        onPress={() =>
                                            setOrigins((current) => [...current, createOrigin()])
                                        }
                                    >
                                        <Plus className='mr-1.5 h-4 w-4' />
                                        Add origin
                                    </Button>
                                </div>
                            </ContentCard>
                        </div>

                        <div className='flex items-center justify-end gap-2'>
                            <Button
                                type='button'
                                variant='ghost'
                                onPress={() => navigate('/sites')}
                            >
                                Cancel
                            </Button>
                            <Button isDisabled={submitting} type='submit'>
                                <ShieldCheck className='mr-1.5 h-4 w-4' />
                                {submitting ? 'Creating…' : 'Create site'}
                            </Button>
                        </div>
                    </div>
                </div>
            </form>
        </div>
    );
}
