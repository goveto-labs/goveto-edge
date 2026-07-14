import type { Certificate } from '@/api';

import { Button, Input, TextArea, useOverlayState } from '@heroui/react';
import { Plus, ShieldCheck, Upload } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, certificatesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

export default function Certificates() {
    const { clusterId } = useCluster();
    const api = useMemo(() => certificatesApi(clusterId), [clusterId]);
    const [certs, setCerts] = useState<Certificate[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const modalState = useOverlayState();
    const [name, setName] = useState('');
    const [certificate, setCertificate] = useState('');
    const [privateKey, setPrivateKey] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [submitError, setSubmitError] = useState('');

    const load = useCallback(async () => {
        if (!clusterId) return;
        setLoading(true);
        try {
            const data = await api.list();
            setCerts(data);
            setError('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load certificates');
        } finally {
            setLoading(false);
        }
    }, [api, clusterId]);

    useEffect(() => {
        load();
    }, [load]);

    const open = () => {
        setName('');
        setCertificate('');
        setPrivateKey('');
        setSubmitError('');
        modalState.open();
    };

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        setSubmitting(true);
        setSubmitError('');
        try {
            await api.create({ name, certificate, private_key: privateKey });
            modalState.close();
            await load();
        } catch (err) {
            setSubmitError(err instanceof ApiError ? err.message : 'Failed to upload certificate');
        } finally {
            setSubmitting(false);
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader subtitle='TLS certificates for site listeners.' title='Certificates' />
                <ContentCard className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to manage certificates.
                    </div>
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='space-y-6'>
            <PageHeader subtitle='TLS certificates for site listeners.' title='Certificates'>
                <Button onPress={open}>
                    <Plus className='mr-2 h-4 w-4' />
                    Upload certificate
                </Button>
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            {loading ? (
                <div className='flex h-64 items-center justify-center text-sm text-muted'>
                    Loading certificates...
                </div>
            ) : (
                <DataTable
                    aria-label='Certificates'
                    empty={certs.length === 0}
                    emptyAction={
                        <Button onPress={open}>
                            <Plus className='mr-2 h-4 w-4' />
                            Upload certificate
                        </Button>
                    }
                    emptyDescription='Upload a TLS certificate before enabling HTTPS for a site.'
                    emptyTitle='No certificates yet'
                >
                    <thead>
                        <tr className='border-b border-border'>
                            <th className='py-3 text-left text-xs font-medium text-muted'>Name</th>
                            <th className='py-3 text-left text-xs font-medium text-muted'>ID</th>
                            <th className='py-3 text-left text-xs font-medium text-muted'>
                                Created at
                            </th>
                        </tr>
                    </thead>
                    <tbody>
                        {certs.map((cert) => (
                            <tr className='border-b border-border last:border-0' key={cert.id}>
                                <td className='flex items-center gap-2 py-3 text-sm font-medium'>
                                    <ShieldCheck className='h-4 w-4 text-success' />
                                    {cert.name}
                                </td>
                                <td className='py-3 font-mono text-xs text-muted'>{cert.id}</td>
                                <td className='py-3 text-sm text-muted'>{cert.created_at}</td>
                            </tr>
                        ))}
                    </tbody>
                </DataTable>
            )}

            <DialogShell
                icon={<Upload className='h-5 w-5' />}
                isOpen={modalState.isOpen}
                size='md'
                subtitle='Upload a TLS certificate and its private key.'
                title='Upload certificate'
                onOpenChange={modalState.setOpen}
            >
                <form className='flex flex-col' onSubmit={handleSubmit}>
                    <div className='space-y-4 p-6'>
                        {submitError && <FormError message={submitError} />}

                        <FormField htmlFor='cert-name' label='Name' required>
                            <Input
                                autoFocus
                                id='cert-name'
                                required
                                variant='secondary'
                                value={name}
                                onChange={(e) => setName(e.target.value)}
                            />
                        </FormField>

                        <FormField htmlFor='cert-pem' label='Certificate PEM' required>
                            <TextArea
                                id='cert-pem'
                                required
                                rows={6}
                                variant='secondary'
                                value={certificate}
                                onChange={(e) => setCertificate(e.target.value)}
                            />
                        </FormField>

                        <FormField htmlFor='cert-key' label='Private key PEM' required>
                            <TextArea
                                id='cert-key'
                                required
                                rows={6}
                                variant='secondary'
                                value={privateKey}
                                onChange={(e) => setPrivateKey(e.target.value)}
                            />
                        </FormField>
                    </div>
                    <DialogFooter>
                        <Button type='button' variant='ghost' onPress={modalState.close}>
                            Cancel
                        </Button>
                        <Button isDisabled={submitting} type='submit' variant='primary'>
                            {submitting ? 'Uploading...' : 'Upload'}
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>
        </div>
    );
}
