import type { Certificate } from '@/api';

import { Button, Input, Label, ListBox, Select, TextArea, useOverlayState } from '@heroui/react';
import { Plus, RefreshCw, RotateCw, Send, ShieldCheck, Trash2, Upload, Zap } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';

import { ApiError, certificatesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';

function message(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

function formatDate(value?: string) {
    return value ? new Date(value).toLocaleString() : '—';
}

export default function Certificates() {
    const { clusterId } = useCluster();
    const api = useMemo(() => certificatesApi(clusterId), [clusterId]);
    const [certs, setCerts] = useState<Certificate[]>([]);
    const [error, setError] = useState('');
    const [busyId, setBusyId] = useState('');
    const [replaceTarget, setReplaceTarget] = useState<Certificate | null>(null);

    const uploadModal = useOverlayState();
    const acmeModal = useOverlayState();
    const [name, setName] = useState('');
    const [certificate, setCertificate] = useState('');
    const [privateKey, setPrivateKey] = useState('');
    const [domains, setDomains] = useState('');
    const [email, setEmail] = useState('');
    const [directoryUrl, setDirectoryUrl] = useState('');
    const [challengeType, setChallengeType] = useState<'HTTP_01' | 'DNS_01'>('HTTP_01');
    const [autoRenew, setAutoRenew] = useState(true);
    const [renewBeforeDays, setRenewBeforeDays] = useState(30);
    const [submitting, setSubmitting] = useState(false);
    const [submitError, setSubmitError] = useState('');

    const load = useCallback(async () => {
        if (!clusterId) return;
        try {
            setCerts(await api.list());
            setError('');
        } catch (err) {
            setError(message(err, 'Failed to load certificates'));
        }
    }, [api, clusterId]);

    useAutoRefresh(load, Boolean(clusterId));

    const reset = () => {
        setName('');
        setCertificate('');
        setPrivateKey('');
        setDomains('');
        setEmail('');
        setDirectoryUrl('');
        setChallengeType('HTTP_01');
        setAutoRenew(true);
        setRenewBeforeDays(30);
        setSubmitError('');
    };

    const openUpload = () => {
        reset();
        setReplaceTarget(null);
        uploadModal.open();
    };

    const openReplace = (cert: Certificate) => {
        reset();
        setName(cert.name);
        setReplaceTarget(cert);
        uploadModal.open();
    };

    const openACME = () => {
        reset();
        acmeModal.open();
    };

    const upload = async (event: React.FormEvent) => {
        event.preventDefault();
        setSubmitting(true);
        setSubmitError('');
        try {
            const payload = { name, certificate, private_key: privateKey };
            if (replaceTarget) await api.replaceMaterial(replaceTarget.id, payload);
            else await api.create(payload);
            uploadModal.close();
            await load();
        } catch (err) {
            setSubmitError(message(err, 'Failed to upload certificate'));
        } finally {
            setSubmitting(false);
        }
    };

    const issue = async (event: React.FormEvent) => {
        event.preventDefault();
        setSubmitting(true);
        setSubmitError('');
        try {
            await api.createACME({
                name,
                domains: domains
                    .split(/[\n,]/)
                    .map((value) => value.trim())
                    .filter(Boolean),
                email,
                directory_url: directoryUrl || undefined,
                challenge_type: challengeType,
                auto_renew: autoRenew,
                renew_before_days: renewBeforeDays,
            });
            acmeModal.close();
            await load();
        } catch (err) {
            setSubmitError(message(err, 'Failed to enqueue ACME issuance'));
        } finally {
            setSubmitting(false);
        }
    };

    const action = async (cert: Certificate, kind: 'renew' | 'reissue' | 'publish' | 'remove') => {
        if (
            kind === 'remove' &&
            !window.confirm(`Delete certificate "${cert.name}" and remove it from attached sites?`)
        )
            return;
        setBusyId(cert.id);
        setError('');
        try {
            if (kind === 'renew') await api.renew(cert.id);
            if (kind === 'reissue') await api.reissue(cert.id);
            if (kind === 'publish') await api.publish(cert.id);
            if (kind === 'remove') await api.remove(cert.id);
            await load();
        } catch (err) {
            setError(message(err, `Failed to ${kind} certificate`));
        } finally {
            setBusyId('');
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
            <PageHeader
                subtitle='Issue, validate, renew, and publish TLS certificates.'
                title='Certificates'
            >
                <Button variant='secondary' onPress={openUpload}>
                    <Upload className='mr-2 h-4 w-4' />
                    Upload PEM
                </Button>
                <Button onPress={openACME}>
                    <Zap className='mr-2 h-4 w-4' />
                    Issue with ACME
                </Button>
            </PageHeader>

            {error && (
                <div className='rounded-lg bg-danger px-4 py-3 text-sm text-danger-foreground'>
                    {error}
                </div>
            )}

            <DataTable
                aria-label='Certificates'
                empty={certs.length === 0}
                emptyAction={
                    <Button onPress={openACME}>
                        <Plus className='mr-2 h-4 w-4' />
                        Issue certificate
                    </Button>
                }
                emptyDescription='Issue with ACME or upload an existing PEM certificate.'
                emptyTitle='No certificates yet'
            >
                <thead>
                    <tr className='border-b border-border'>
                        <th>Name</th>
                        <th>Status</th>
                        <th>Domains</th>
                        <th>Expires</th>
                        <th>Source</th>
                        <th className='text-right'>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    {certs.map((cert) => {
                        const busy = busyId === cert.id;
                        const lifecycleError = cert.last_renewal_error || cert.last_publish_error;
                        return (
                            <tr className='border-b border-border last:border-0' key={cert.id}>
                                <td>
                                    <div className='flex items-center gap-2 text-sm font-semibold'>
                                        <ShieldCheck
                                            className={`h-4 w-4 ${cert.status === 'ACTIVE' ? 'text-success' : 'text-muted'}`}
                                        />
                                        {cert.name}
                                    </div>
                                    <div className='mt-1 max-w-64 truncate font-mono text-[11px] text-muted'>
                                        {cert.fingerprint || cert.id}
                                    </div>
                                    {lifecycleError && (
                                        <div
                                            className='mt-1 max-w-72 truncate text-xs text-danger'
                                            title={lifecycleError}
                                        >
                                            {lifecycleError}
                                        </div>
                                    )}
                                </td>
                                <td>
                                    <StatusBadge status={cert.status} />
                                </td>
                                <td>
                                    <div className='max-w-72 text-sm'>
                                        {cert.domains.slice(0, 3).join(', ') || 'Awaiting issuance'}
                                    </div>
                                    {cert.domains.length > 3 && (
                                        <div className='text-xs text-muted'>
                                            +{cert.domains.length - 3} more
                                        </div>
                                    )}
                                </td>
                                <td className='whitespace-nowrap text-sm text-muted'>
                                    {formatDate(cert.expires_at)}
                                </td>
                                <td className='text-sm'>
                                    {cert.source === 'ACME'
                                        ? `${cert.acme_challenge_type?.replace('_', '-') || 'ACME'}${cert.auto_renew ? ' · auto-renew' : ''}`
                                        : 'Manual'}
                                </td>
                                <td>
                                    <div className='flex flex-wrap justify-end gap-2'>
                                        {cert.source === 'ACME' && (
                                            <>
                                                <Button
                                                    isDisabled={busy}
                                                    size='sm'
                                                    variant='secondary'
                                                    onPress={() => void action(cert, 'renew')}
                                                >
                                                    <RefreshCw className='mr-1.5 h-3.5 w-3.5' />
                                                    Renew
                                                </Button>
                                                <Button
                                                    isDisabled={busy}
                                                    size='sm'
                                                    variant='secondary'
                                                    onPress={() => void action(cert, 'reissue')}
                                                >
                                                    <RotateCw className='mr-1.5 h-3.5 w-3.5' />
                                                    Reissue
                                                </Button>
                                            </>
                                        )}
                                        {cert.source === 'MANUAL' && (
                                            <Button
                                                isDisabled={busy}
                                                size='sm'
                                                variant='secondary'
                                                onPress={() => openReplace(cert)}
                                            >
                                                <Upload className='mr-1.5 h-3.5 w-3.5' />
                                                Replace
                                            </Button>
                                        )}
                                        <Button
                                            isDisabled={busy || cert.status === 'PENDING'}
                                            size='sm'
                                            variant='secondary'
                                            onPress={() => void action(cert, 'publish')}
                                        >
                                            <Send className='mr-1.5 h-3.5 w-3.5' />
                                            Publish
                                        </Button>
                                        <Button
                                            isDisabled={busy}
                                            size='sm'
                                            variant='danger'
                                            onPress={() => void action(cert, 'remove')}
                                        >
                                            <Trash2 className='h-3.5 w-3.5' />
                                        </Button>
                                    </div>
                                </td>
                            </tr>
                        );
                    })}
                </tbody>
            </DataTable>

            <DialogShell
                icon={<Upload className='h-5 w-5' />}
                isOpen={uploadModal.isOpen}
                size='md'
                subtitle='The private key is encrypted before storage.'
                title={replaceTarget ? 'Replace certificate' : 'Upload certificate'}
                onOpenChange={uploadModal.setOpen}
            >
                <form className='flex flex-col' onSubmit={upload}>
                    <div className='space-y-4 p-6'>
                        {submitError && <FormError message={submitError} />}
                        <FormField htmlFor='cert-name' label='Name' required>
                            <Input
                                autoFocus
                                id='cert-name'
                                disabled={Boolean(replaceTarget)}
                                required
                                variant='secondary'
                                value={name}
                                onChange={(event) => setName(event.target.value)}
                            />
                        </FormField>
                        <FormField htmlFor='cert-pem' label='Certificate chain PEM' required>
                            <TextArea
                                id='cert-pem'
                                required
                                rows={6}
                                variant='secondary'
                                value={certificate}
                                onChange={(event) => setCertificate(event.target.value)}
                            />
                        </FormField>
                        <FormField htmlFor='cert-key' label='Private key PEM' required>
                            <TextArea
                                id='cert-key'
                                required
                                rows={6}
                                variant='secondary'
                                value={privateKey}
                                onChange={(event) => setPrivateKey(event.target.value)}
                            />
                        </FormField>
                    </div>
                    <DialogFooter>
                        <Button type='button' variant='ghost' onPress={uploadModal.close}>
                            Cancel
                        </Button>
                        <Button isDisabled={submitting} type='submit' variant='primary'>
                            {submitting ? 'Validating…' : replaceTarget ? 'Replace' : 'Upload'}
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>

            <DialogShell
                icon={<Zap className='h-5 w-5' />}
                isOpen={acmeModal.isOpen}
                size='lg'
                subtitle='HTTP-01 uses an attached site; wildcard names require DNS-01.'
                title='Issue with ACME'
                onOpenChange={acmeModal.setOpen}
            >
                <form className='flex flex-col' onSubmit={issue}>
                    <div className='grid gap-4 p-6 md:grid-cols-2'>
                        {submitError && (
                            <div className='md:col-span-2'>
                                <FormError message={submitError} />
                            </div>
                        )}
                        <FormField htmlFor='acme-name' label='Name' required>
                            <Input
                                autoFocus
                                id='acme-name'
                                required
                                variant='secondary'
                                value={name}
                                onChange={(event) => setName(event.target.value)}
                            />
                        </FormField>
                        <FormField htmlFor='acme-email' label='ACME account email' required>
                            <Input
                                id='acme-email'
                                required
                                type='email'
                                variant='secondary'
                                value={email}
                                onChange={(event) => setEmail(event.target.value)}
                            />
                        </FormField>
                        <div className='md:col-span-2'>
                            <FormField
                                htmlFor='acme-domains'
                                label='Domains'
                                hint='One per line or comma-separated. Example: example.com, *.example.com'
                                required
                            >
                                <TextArea
                                    id='acme-domains'
                                    required
                                    rows={4}
                                    variant='secondary'
                                    value={domains}
                                    onChange={(event) => setDomains(event.target.value)}
                                />
                            </FormField>
                        </div>
                        <Select
                            value={challengeType}
                            variant='secondary'
                            onChange={(key) =>
                                setChallengeType(String(key) as 'HTTP_01' | 'DNS_01')
                            }
                        >
                            <Label>Challenge</Label>
                            <Select.Trigger>
                                <Select.Value />
                            </Select.Trigger>
                            <Select.Popover>
                                <ListBox>
                                    <ListBox.Item id='HTTP_01'>HTTP-01</ListBox.Item>
                                    <ListBox.Item id='DNS_01'>DNS-01</ListBox.Item>
                                </ListBox>
                            </Select.Popover>
                        </Select>
                        <FormField htmlFor='renew-days' label='Renew before expiry'>
                            <Input
                                id='renew-days'
                                max={90}
                                min={1}
                                type='number'
                                variant='secondary'
                                value={String(renewBeforeDays)}
                                onChange={(event) => setRenewBeforeDays(Number(event.target.value))}
                            />
                        </FormField>
                        <div className='md:col-span-2'>
                            <FormField
                                htmlFor='directory-url'
                                label='ACME directory URL'
                                hint="Leave blank for Let's Encrypt production."
                            >
                                <Input
                                    id='directory-url'
                                    type='url'
                                    variant='secondary'
                                    value={directoryUrl}
                                    onChange={(event) => setDirectoryUrl(event.target.value)}
                                />
                            </FormField>
                        </div>
                        <div className='flex items-center justify-between rounded-lg border border-border px-4 py-3 text-sm md:col-span-2'>
                            <span>
                                <span className='font-medium'>Automatic renewal</span>
                                <span className='mt-0.5 block text-xs text-muted'>
                                    Retries failures and republishes the previous certificate on
                                    rollout errors.
                                </span>
                            </span>
                            <ToggleSwitch
                                isSelected={autoRenew}
                                label='Automatic renewal'
                                onChange={setAutoRenew}
                            />
                        </div>
                    </div>
                    <DialogFooter>
                        <Button type='button' variant='ghost' onPress={acmeModal.close}>
                            Cancel
                        </Button>
                        <Button isDisabled={submitting} type='submit' variant='primary'>
                            {submitting ? 'Queueing…' : 'Issue certificate'}
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>
        </div>
    );
}
