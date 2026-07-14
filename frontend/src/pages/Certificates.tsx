import type { Certificate } from '@/api';

import { Button, Card, Input, Label, Modal, Table, TextArea, useOverlayState } from '@heroui/react';
import { Plus, ShieldCheck, Upload } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, certificatesApi } from '@/api';
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
                <Card className='p-8 text-center'>
                    <div className='text-sm text-muted'>
                        Select a cluster in the header to manage certificates.
                    </div>
                </Card>
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
                <Card className='overflow-hidden'>
                    <Table>
                        <Table.ScrollContainer>
                            <Table.Content aria-label='Certificates'>
                                <Table.Header>
                                    <Table.Column isRowHeader>Name</Table.Column>
                                    <Table.Column>ID</Table.Column>
                                    <Table.Column>Created at</Table.Column>
                                </Table.Header>
                                <Table.Body>
                                    {certs.map((cert) => (
                                        <Table.Row key={cert.id} id={cert.id}>
                                            <Table.Cell className='flex items-center gap-2 font-medium'>
                                                <ShieldCheck className='h-4 w-4 text-success' />
                                                {cert.name}
                                            </Table.Cell>
                                            <Table.Cell className='font-mono text-xs'>
                                                {cert.id}
                                            </Table.Cell>
                                            <Table.Cell>{cert.created_at}</Table.Cell>
                                        </Table.Row>
                                    ))}
                                </Table.Body>
                            </Table.Content>
                        </Table.ScrollContainer>
                    </Table>
                </Card>
            )}

            <Modal isOpen={modalState.isOpen} onOpenChange={modalState.setOpen}>
                <Modal.Backdrop>
                    <Modal.Container size='md'>
                        <Modal.Dialog>
                            <form className='space-y-4' onSubmit={handleSubmit}>
                                <Modal.Header>
                                    <Modal.Heading className='flex items-center gap-2'>
                                        <Upload className='h-5 w-5' />
                                        Upload certificate
                                    </Modal.Heading>
                                </Modal.Header>
                                <Modal.Body className='space-y-4'>
                                    {submitError && (
                                        <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                                            {submitError}
                                        </div>
                                    )}
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='cert-name'>Name</Label>
                                        <Input
                                            variant='secondary'
                                            id='cert-name'
                                            required
                                            value={name}
                                            onChange={(e) => setName(e.target.value)}
                                        />
                                    </div>
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='cert-pem'>Certificate PEM</Label>
                                        <TextArea
                                            variant='secondary'
                                            id='cert-pem'
                                            required
                                            rows={6}
                                            value={certificate}
                                            onChange={(e) => setCertificate(e.target.value)}
                                        />
                                    </div>
                                    <div className='flex flex-col gap-1'>
                                        <Label htmlFor='cert-key'>Private key PEM</Label>
                                        <TextArea
                                            variant='secondary'
                                            id='cert-key'
                                            required
                                            rows={6}
                                            value={privateKey}
                                            onChange={(e) => setPrivateKey(e.target.value)}
                                        />
                                    </div>
                                </Modal.Body>
                                <Modal.Footer>
                                    <Button type='button' variant='ghost' onPress={modalState.close}>
                                        Cancel
                                    </Button>
                                    <Button isDisabled={submitting} type='submit' variant='primary'>
                                        {submitting ? 'Uploading...' : 'Upload'}
                                    </Button>
                                </Modal.Footer>
                            </form>
                        </Modal.Dialog>
                    </Modal.Container>
                </Modal.Backdrop>
            </Modal>
        </div>
    );
}
