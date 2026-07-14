import type { ClusterGroup, ClusterRegion, DNSLine } from '@/api';

import { Button, Input, ListBox, Select, Spinner } from '@heroui/react';
import { ArrowLeft, Server } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError, clusterApi, nodesApi } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { FormError, FormField } from '@/components/FormField.tsx';
import { PageHeader } from '@/components/PageHeader.tsx';
import { useCluster } from '@/hooks/useCluster.ts';

function parseCommaList(value: string) {
    return value
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
}

export default function CreateNode() {
    const navigate = useNavigate();
    const { clusterId } = useCluster();
    const cluster = useMemo(() => clusterApi(clusterId), [clusterId]);
    const nodeApi = useMemo(() => nodesApi(clusterId), [clusterId]);

    const [dnsLines, setDnsLines] = useState<DNSLine[]>([]);
    const [groups, setGroups] = useState<ClusterGroup[]>([]);
    const [regions, setRegions] = useState<ClusterRegion[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const [submitting, setSubmitting] = useState(false);
    const [submitError, setSubmitError] = useState('');
    const [name, setName] = useState('');
    const [addresses, setAddresses] = useState('');
    const [dnsLineIds, setDnsLineIds] = useState<Set<string>>(new Set());
    const [groupId, setGroupId] = useState('');
    const [regionId, setRegionId] = useState('');
    const [sshIp, setSshIp] = useState('');
    const [sshPort, setSshPort] = useState('22');
    const [sshUser, setSshUser] = useState('');
    const [sshPassword, setSshPassword] = useState('');
    const [sshKey, setSshKey] = useState('');
    const [sshPassphrase, setSshPassphrase] = useState('');

    const loadOptions = useCallback(async () => {
        if (!clusterId) return;
        setLoading(true);
        try {
            const [d, g, r] = await Promise.all([
                cluster.dnsLines(),
                cluster.groups(),
                cluster.regions(),
            ]);
            setDnsLines(d);
            setGroups(g);
            setRegions(r);
            setError('');
        } catch (err) {
            setError(err instanceof ApiError ? err.message : 'Failed to load options');
        } finally {
            setLoading(false);
        }
    }, [cluster, clusterId]);

    useEffect(() => {
        loadOptions();
    }, [loadOptions]);

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!clusterId) return;
        setSubmitting(true);
        setSubmitError('');
        try {
            await nodeApi.create({
                name,
                addresses: parseCommaList(addresses),
                dns_line_ids: Array.from(dnsLineIds),
                group_id: groupId || undefined,
                region_id: regionId || undefined,
                ssh: {
                    entry_ip: sshIp,
                    port: Number(sshPort) || 22,
                    user: sshUser,
                    password: sshPassword || undefined,
                    private_key: sshKey || undefined,
                    passphrase: sshPassphrase || undefined,
                },
            });
            navigate('/nodes');
        } catch (err) {
            setSubmitError(err instanceof ApiError ? err.message : 'Failed to create node');
        } finally {
            setSubmitting(false);
        }
    };

    if (!clusterId) {
        return (
            <div className='space-y-6'>
                <PageHeader
                    subtitle='Add an edge node and configure SSH access.'
                    title='Create node'
                />
                <ContentCard className='p-8 text-center'>
                    <div className='text-sm text-muted'>Select a cluster to create a node.</div>
                </ContentCard>
            </div>
        );
    }

    return (
        <div className='mx-auto max-w-3xl space-y-6'>
            <PageHeader
                actions={
                    <Button variant='ghost' onPress={() => navigate('/nodes')}>
                        <ArrowLeft className='mr-1.5 h-4 w-4' />
                        Back to nodes
                    </Button>
                }
                subtitle='Add an edge node and configure SSH access for remote management.'
                title='Create node'
            />

            {error && <FormError message={error} />}

            {loading ? (
                <div className='flex h-64 items-center justify-center'>
                    <Spinner />
                </div>
            ) : (
                <ContentCard>
                    <form className='space-y-6' onSubmit={handleSubmit}>
                        {submitError && <FormError message={submitError} />}

                        <FormField htmlFor='node-name' label='Name' required>
                            <Input
                                autoFocus
                                id='node-name'
                                required
                                variant='secondary'
                                value={name}
                                onChange={(e) => setName(e.target.value)}
                            />
                        </FormField>

                        <FormField
                            hint='Comma separated list of public addresses'
                            htmlFor='node-addresses'
                            label='Addresses'
                            required
                        >
                            <Input
                                id='node-addresses'
                                required
                                variant='secondary'
                                value={addresses}
                                onChange={(e) => setAddresses(e.target.value)}
                            />
                        </FormField>

                        <div className='grid grid-cols-1 gap-4 sm:grid-cols-3'>
                            <FormField className='sm:col-span-2' label='DNS lines'>
                                <Select
                                    selectionMode='multiple'
                                    value={Array.from(dnsLineIds)}
                                    variant='secondary'
                                    onChange={(keys) => setDnsLineIds(new Set(keys as string[]))}
                                >
                                    <Select.Trigger>
                                        <Select.Value />
                                    </Select.Trigger>
                                    <Select.Popover>
                                        <ListBox>
                                            {dnsLines.map((line) => (
                                                <ListBox.Item
                                                    id={line.id}
                                                    key={line.id}
                                                    textValue={line.name}
                                                >
                                                    {line.name}
                                                </ListBox.Item>
                                            ))}
                                        </ListBox>
                                    </Select.Popover>
                                </Select>
                            </FormField>

                            <FormField label='Group (optional)'>
                                <Select
                                    value={groupId || null}
                                    variant='secondary'
                                    onChange={(key) => setGroupId(String(key ?? ''))}
                                >
                                    <Select.Trigger>
                                        <Select.Value />
                                    </Select.Trigger>
                                    <Select.Popover>
                                        <ListBox>
                                            {groups.map((g) => (
                                                <ListBox.Item
                                                    id={g.id}
                                                    key={g.id}
                                                    textValue={g.name}
                                                >
                                                    {g.name}
                                                </ListBox.Item>
                                            ))}
                                        </ListBox>
                                    </Select.Popover>
                                </Select>
                            </FormField>
                        </div>

                        <FormField label='Region (optional)'>
                            <Select
                                value={regionId || null}
                                variant='secondary'
                                onChange={(key) => setRegionId(String(key ?? ''))}
                            >
                                <Select.Trigger>
                                    <Select.Value />
                                </Select.Trigger>
                                <Select.Popover>
                                    <ListBox>
                                        {regions.map((r) => (
                                            <ListBox.Item id={r.id} key={r.id} textValue={r.name}>
                                                {r.name}
                                            </ListBox.Item>
                                        ))}
                                    </ListBox>
                                </Select.Popover>
                            </Select>
                        </FormField>

                        <div className='border-t border-border pt-4'>
                            <div className='mb-3 flex items-center gap-2 text-sm font-semibold'>
                                <Server className='h-4 w-4 text-muted' />
                                SSH access
                            </div>
                            <div className='grid grid-cols-1 gap-4 sm:grid-cols-2'>
                                <FormField htmlFor='node-ssh-ip' label='Entry IP' required>
                                    <Input
                                        id='node-ssh-ip'
                                        required
                                        variant='secondary'
                                        value={sshIp}
                                        onChange={(e) => setSshIp(e.target.value)}
                                    />
                                </FormField>
                                <FormField htmlFor='node-ssh-port' label='Port' required>
                                    <Input
                                        id='node-ssh-port'
                                        required
                                        type='number'
                                        variant='secondary'
                                        value={sshPort}
                                        onChange={(e) => setSshPort(e.target.value)}
                                    />
                                </FormField>
                            </div>
                            <div className='mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2'>
                                <FormField htmlFor='node-ssh-user' label='User' required>
                                    <Input
                                        id='node-ssh-user'
                                        required
                                        variant='secondary'
                                        value={sshUser}
                                        onChange={(e) => setSshUser(e.target.value)}
                                    />
                                </FormField>
                                <FormField htmlFor='node-ssh-password' label='Password'>
                                    <Input
                                        id='node-ssh-password'
                                        type='password'
                                        variant='secondary'
                                        value={sshPassword}
                                        onChange={(e) => setSshPassword(e.target.value)}
                                    />
                                </FormField>
                            </div>
                            <div className='mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2'>
                                <FormField
                                    hint='Path to private key file on the node'
                                    htmlFor='node-ssh-key'
                                    label='Private key path'
                                >
                                    <Input
                                        id='node-ssh-key'
                                        variant='secondary'
                                        value={sshKey}
                                        onChange={(e) => setSshKey(e.target.value)}
                                    />
                                </FormField>
                                <FormField htmlFor='node-ssh-passphrase' label='Key passphrase'>
                                    <Input
                                        id='node-ssh-passphrase'
                                        type='password'
                                        variant='secondary'
                                        value={sshPassphrase}
                                        onChange={(e) => setSshPassphrase(e.target.value)}
                                    />
                                </FormField>
                            </div>
                        </div>

                        <div className='flex justify-end gap-2 border-t border-border pt-4'>
                            <Button
                                type='button'
                                variant='ghost'
                                onPress={() => navigate('/nodes')}
                            >
                                Cancel
                            </Button>
                            <Button isDisabled={submitting} type='submit' variant='primary'>
                                {submitting ? 'Creating...' : 'Create node'}
                            </Button>
                        </div>
                    </form>
                </ContentCard>
            )}
        </div>
    );
}
