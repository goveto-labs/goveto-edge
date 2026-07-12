import type { Certificate } from '@/api';

import { Button, Card, Input, Label, Modal, Table, TextArea, useOverlayState } from '@heroui/react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiError, certificatesApi } from '@/api';
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
            <div className='text-sm text-muted'>
                Select a cluster in the header to manage certificates.
            </div>
        );
    }

    return (
        <div className='space-y-4'>
            <div className='flex items-center justify-between'>
                <h1 className='text-2xl font-bold'>Certificates</h1>
                <Button onPress={open}>Upload certificate</Button>
            </div>

            {error && (
                <div className='rounded-md bg-danger p-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}
            {loading && <div className='text-sm text-muted'>Loading...</div>}

            <Card className='overflow-hidden'>
                <Table>
                    <Table.Header>
                        <Table.Column>Name</Table.Column>
                        <Table.Column>ID</Table.Column>
                        <Table.Column>Created at</Table.Column>
                    </Table.Header>
                    <Table.Body>
                        {certs.map((cert) => (
                            <Table.Row key={cert.id} id={cert.id}>
                                <Table.Cell>{cert.name}</Table.Cell>
                                <Table.Cell className='font-mono text-xs'>{cert.id}</Table.Cell>
                                <Table.Cell>{cert.created_at}</Table.Cell>
                            </Table.Row>
                        ))}
                    </Table.Body>
                </Table>
            </Card>

            <Modal isOpen={modalState.isOpen} onOpenChange={modalState.setOpen}>
                <Modal.Container size='md'>
                    <Modal.Dialog>
                        <form className='space-y-4' onSubmit={handleSubmit}>
                            <Modal.Header>
                                <Modal.Heading>Upload certificate</Modal.Heading>
                            </Modal.Header>
                            <Modal.Body className='space-y-4'>
                                {submitError && (
                                    <div className='rounded-md bg-danger p-3 text-sm text-danger-foreground'>
                                        {submitError}
                                    </div>
                                )}
                                <div>
                                    <Label htmlFor='cert-name'>Name</Label>
                                    <Input
                                        id='cert-name'
                                        required
                                        value={name}
                                        onChange={(e) => setName(e.target.value)}
                                    />
                                </div>
                                <div>
                                    <Label htmlFor='cert-pem'>Certificate PEM</Label>
                                    <TextArea
                                        id='cert-pem'
                                        required
                                        rows={6}
                                        value={certificate}
                                        onChange={(e) => setCertificate(e.target.value)}
                                    />
                                </div>
                                <div>
                                    <Label htmlFor='cert-key'>Private key PEM</Label>
                                    <TextArea
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
            </Modal>
        </div>
    );
}
