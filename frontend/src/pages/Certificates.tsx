import type { Certificate, DNSZone } from '@/api';

import { Button, Input, TextArea, useOverlayState } from '@heroui/react';
import { Plus, RefreshCw, RotateCw, Send, ShieldCheck, Trash2, Upload, Zap } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';

import { ApiError, certificatesApi, dnsApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { DataTable } from '@/components/DataTable.tsx';
import { DialogFooter, DialogShell } from '@/components/DialogShell.tsx';
import { DomainAddField } from '@/components/DomainAddField.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { StatusBadge } from '@/components/StatusBadge.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { useAutoRefresh } from '@/hooks/useAutoRefresh.ts';
import { useCluster } from '@/hooks/useCluster.ts';
import { canManageCluster } from '@/utils/rbac.ts';

const LETS_ENCRYPT_PROD = 'https://acme-v02.api.letsencrypt.org/directory';
const LETS_ENCRYPT_STAGING = 'https://acme-staging-v02.api.letsencrypt.org/directory';

function message(error: unknown, fallback: string) {
    return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

function formatDate(value?: string) {
    return value ? new Date(value).toLocaleString() : '—';
}

function normalizeDomain(value: string) {
    return value.trim().toLowerCase().replace(/\.$/, '');
}

function apexDomain(value: string) {
    return value.startsWith('*.') ? value.slice(2) : value;
}

function asciiHostname(value: string) {
    const normalized = apexDomain(normalizeDomain(value));
    try {
        return new URL(`http://${normalized}`).hostname.replace(/\.$/, '');
    } catch {
        return normalized;
    }
}

function zoneCoversDomain(zone: string, domain: string) {
    const host = asciiHostname(domain);
    const normalizedZone = asciiHostname(zone);
    return host === normalizedZone || host.endsWith(`.${normalizedZone}`);
}

function bestZoneForDomain(zones: DNSZone[], domain: string) {
    let best: DNSZone | null = null;
    for (const zone of zones) {
        if (!zone.enabled) continue;
        if (!zoneCoversDomain(zone.zone, domain)) continue;
        if (!best || zone.zone.length > best.zone.length) best = zone;
    }
    return best;
}

function suggestName(domains: string[]) {
    if (domains.length === 0) return '';
    const primary = apexDomain(domains[0]);
    if (domains.some((domain) => domain.startsWith('*.'))) return `${primary}-wildcard`;
    if (domains.length === 1) return primary;
    return `${primary}-san`;
}

export default function Certificates() {
    const { clusterId, clusters } = useCluster();
    const canManage = canManageCluster(
        clusters.find((clusterItem) => clusterItem.id === clusterId)?.role
    );
    const api = useMemo(() => certificatesApi(clusterId), [clusterId]);
    const dns = useMemo(() => dnsApi(clusterId), [clusterId]);
    const [certs, setCerts] = useState<Certificate[]>([]);
    const [zones, setZones] = useState<DNSZone[]>([]);
    const [error, setError] = useState('');
    const [busyId, setBusyId] = useState('');
    const [replaceTarget, setReplaceTarget] = useState<Certificate | null>(null);

    const uploadModal = useOverlayState();
    const acmeModal = useOverlayState();
    const [name, setName] = useState('');
    const [nameTouched, setNameTouched] = useState(false);
    const [certificate, setCertificate] = useState('');
    const [privateKey, setPrivateKey] = useState('');
    const [domains, setDomains] = useState<string[]>([]);
    const [email, setEmail] = useState('');
    const [directoryPreset, setDirectoryPreset] = useState<'production' | 'staging' | 'custom'>(
        'production'
    );
    const [directoryUrl, setDirectoryUrl] = useState('');
    const [challengeType, setChallengeType] = useState<'HTTP_01' | 'DNS_01'>('HTTP_01');
    const [challengeTouched, setChallengeTouched] = useState(false);
    const [autoRenew, setAutoRenew] = useState(true);
    const [renewBeforeDays, setRenewBeforeDays] = useState(30);
    const [submitting, setSubmitting] = useState(false);
    const [submitError, setSubmitError] = useState('');

    const hasWildcard = domains.some((domain) => domain.startsWith('*.'));
    const effectiveChallenge = hasWildcard ? 'DNS_01' : challengeType;
    const domainCoverage = useMemo(
        () =>
            domains.map((domain) => ({
                domain,
                zone: bestZoneForDomain(zones, domain),
            })),
        [domains, zones]
    );
    const uncoveredDomains = domainCoverage.filter((item) => !item.zone).map((item) => item.domain);
    const enabledZones = zones.filter((zone) => zone.enabled);

    const load = useCallback(async () => {
        if (!clusterId) return;
        try {
            const [certificateData, dnsConfig] = await Promise.all([
                api.list(),
                dns.config().catch(() => null),
            ]);
            setCerts(certificateData);
            setZones(dnsConfig?.zones ?? []);
            setError('');
        } catch (err) {
            setError(message(err, 'Failed to load certificates'));
        }
    }, [api, clusterId, dns]);

    useAutoRefresh(load, Boolean(clusterId));

    useEffect(() => {
        if (!nameTouched) setName(suggestName(domains));
    }, [domains, nameTouched]);

    useEffect(() => {
        if (hasWildcard && !challengeTouched) setChallengeType('DNS_01');
    }, [challengeTouched, hasWildcard]);

    const reset = () => {
        setName('');
        setNameTouched(false);
        setCertificate('');
        setPrivateKey('');
        setDomains([]);
        const lastEmail = certs.find((cert) => cert.acme_email)?.acme_email || '';
        setEmail(lastEmail);
        setDirectoryPreset('production');
        setDirectoryUrl('');
        setChallengeType('HTTP_01');
        setChallengeTouched(false);
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

    const resolvedDirectoryUrl = () => {
        if (directoryPreset === 'production') return undefined;
        if (directoryPreset === 'staging') return LETS_ENCRYPT_STAGING;
        return directoryUrl || undefined;
    };

    const issue = async (event: React.FormEvent) => {
        event.preventDefault();
        if (domains.length === 0) {
            setSubmitError('Add at least one domain.');
            return;
        }
        if (effectiveChallenge === 'DNS_01' && uncoveredDomains.length > 0) {
            setSubmitError(
                `No configured DNS zone covers: ${uncoveredDomains.join(', ')}. Add the zone under DNS settings first.`
            );
            return;
        }
        if (directoryPreset === 'custom') {
            if (!directoryUrl.trim()) {
                setSubmitError('Enter a custom ACME directory URL.');
                return;
            }
            try {
                const parsed = new URL(directoryUrl);
                if (parsed.protocol !== 'https:') {
                    setSubmitError('ACME directory URL must use HTTPS.');
                    return;
                }
            } catch {
                setSubmitError('ACME directory URL is invalid.');
                return;
            }
        }
        setSubmitting(true);
        setSubmitError('');
        try {
            await api.createACME({
                name: name.trim() || suggestName(domains),
                domains,
                email,
                directory_url: resolvedDirectoryUrl(),
                challenge_type: effectiveChallenge,
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
                {canManage && (
                    <>
                        <Button variant='secondary' onPress={openUpload}>
                            <Upload className='mr-2 h-4 w-4' />
                            Upload PEM
                        </Button>
                        <Button onPress={openACME}>
                            <Zap className='mr-2 h-4 w-4' />
                            Issue with ACME
                        </Button>
                    </>
                )}
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
                    canManage ? (
                        <Button onPress={openACME}>
                            <Plus className='mr-2 h-4 w-4' />
                            Issue certificate
                        </Button>
                    ) : undefined
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
                                    {canManage && (
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
                                    )}
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
                subtitle='Add domains with chips, then choose HTTP-01 or DNS-01. Wildcards always use DNS-01.'
                title='Issue with ACME'
                onOpenChange={acmeModal.setOpen}
            >
                <form className='flex flex-col' onSubmit={issue}>
                    <div className='grid max-h-[75vh] gap-4 overflow-y-auto p-6 md:grid-cols-2'>
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
                                onChange={(event) => {
                                    setNameTouched(true);
                                    setName(event.target.value);
                                }}
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
                                label='Domains'
                                hint='Add one domain, paste many, or include matching wildcards.'
                                required
                            >
                                <DomainAddField
                                    allowWildcard
                                    addLabel='Add domains'
                                    emptyLabel='No domains added yet'
                                    value={domains}
                                    onChange={setDomains}
                                />
                            </FormField>
                        </div>
                        {domains.length > 0 && effectiveChallenge === 'DNS_01' && (
                            <div className='space-y-2 rounded-lg border border-border bg-surface-secondary p-4 md:col-span-2'>
                                <div className='text-sm font-medium'>DNS-01 zone coverage</div>
                                {enabledZones.length === 0 ? (
                                    <p className='text-sm text-danger'>
                                        No DNS zones are configured.{' '}
                                        <Link className='underline' to='/dns'>
                                            Open DNS settings
                                        </Link>{' '}
                                        to add a provider zone before issuing.
                                    </p>
                                ) : (
                                    <div className='space-y-1.5'>
                                        {domainCoverage.map((item) => (
                                            <div
                                                className='flex flex-wrap items-center justify-between gap-2 text-sm'
                                                key={item.domain}
                                            >
                                                <span className='font-mono text-xs'>
                                                    {item.domain}
                                                </span>
                                                {item.zone ? (
                                                    <span className='text-xs text-muted'>
                                                        {item.zone.type === 'ALIYUN'
                                                            ? 'Aliyun'
                                                            : 'Cloudflare'}{' '}
                                                        · {item.zone.zone}
                                                        {item.zone.kind === 'ENDPOINT'
                                                            ? ' (endpoint)'
                                                            : ''}
                                                    </span>
                                                ) : (
                                                    <span className='text-xs text-danger'>
                                                        No matching DNS zone
                                                    </span>
                                                )}
                                            </div>
                                        ))}
                                        {uncoveredDomains.length > 0 && (
                                            <p className='pt-1 text-xs text-danger'>
                                                Add missing zones in{' '}
                                                <Link className='underline' to='/dns'>
                                                    DNS settings
                                                </Link>{' '}
                                                before continuing.
                                            </p>
                                        )}
                                    </div>
                                )}
                            </div>
                        )}
                        {domains.length > 0 && effectiveChallenge === 'HTTP_01' && (
                            <div className='rounded-lg border border-border bg-surface-secondary px-4 py-3 text-sm text-muted md:col-span-2'>
                                HTTP-01 requires each domain to already be attached to a published
                                site on this cluster.
                            </div>
                        )}
                        <SelectField
                            label='Challenge'
                            options={[
                                {
                                    id: 'HTTP_01',
                                    label: 'HTTP-01 · domain attached to a site',
                                },
                                {
                                    id: 'DNS_01',
                                    label: 'DNS-01 · provider zone / wildcards',
                                },
                            ]}
                            value={effectiveChallenge}
                            variant='secondary'
                            isDisabled={hasWildcard}
                            onChange={(value) => {
                                setChallengeTouched(true);
                                setChallengeType(value as 'HTTP_01' | 'DNS_01');
                            }}
                        />
                        <FormField htmlFor='renew-days' label='Renew before expiry (days)'>
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
                        <SelectField
                            label='ACME directory'
                            options={[
                                { id: 'production', label: "Let's Encrypt production" },
                                { id: 'staging', label: "Let's Encrypt staging" },
                                { id: 'custom', label: 'Custom HTTPS directory URL' },
                            ]}
                            value={directoryPreset}
                            variant='secondary'
                            onChange={(value) =>
                                setDirectoryPreset(value as 'production' | 'staging' | 'custom')
                            }
                        />
                        {directoryPreset === 'custom' ? (
                            <FormField
                                htmlFor='directory-url'
                                label='Custom directory URL'
                                hint={`Example: ${LETS_ENCRYPT_PROD}`}
                                required
                            >
                                <Input
                                    id='directory-url'
                                    type='url'
                                    variant='secondary'
                                    value={directoryUrl}
                                    onChange={(event) => setDirectoryUrl(event.target.value)}
                                />
                            </FormField>
                        ) : (
                            <div className='rounded-lg border border-border px-4 py-3 text-sm text-muted'>
                                {directoryPreset === 'production'
                                    ? "Uses Let's Encrypt production. Certificates are trusted by browsers."
                                    : "Uses Let's Encrypt staging. Ideal for testing rate limits and DNS-01 wiring."}
                            </div>
                        )}
                        {hasWildcard && (
                            <div className='rounded-lg border border-border bg-surface-secondary px-4 py-3 text-sm text-muted md:col-span-2'>
                                Wildcard names were detected, so the challenge is locked to DNS-01.
                            </div>
                        )}
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
                        <Button
                            isDisabled={
                                submitting ||
                                domains.length === 0 ||
                                (effectiveChallenge === 'DNS_01' && uncoveredDomains.length > 0)
                            }
                            type='submit'
                            variant='primary'
                        >
                            {submitting ? 'Queueing…' : 'Issue certificate'}
                        </Button>
                    </DialogFooter>
                </form>
            </DialogShell>
        </div>
    );
}
