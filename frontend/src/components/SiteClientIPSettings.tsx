import type { SecurityPolicy } from '@/api';

import { TextArea } from '@heroui/react';
import { Network } from 'lucide-react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { FormField } from '@/components/FormField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

const allSourceNetworks = ['0.0.0.0/0', '::/0'] as const;

export function SiteClientIPSettings({
    waf,
    onChange,
}: {
    waf: SecurityPolicy['waf'];
    onChange: (waf: SecurityPolicy['waf']) => void;
}) {
    const hasTrustedProxy = waf.trusted_proxies.some((value) => value.trim());
    const trustsAllSources = allSourceNetworks.every((network) =>
        waf.trusted_proxies.includes(network)
    );
    const setTrustsAllSources = (enabled: boolean) => {
        const specificNetworks = waf.trusted_proxies.filter(
            (network) => !allSourceNetworks.includes(network as (typeof allSourceNetworks)[number])
        );
        onChange({
            ...waf,
            trusted_proxies: enabled
                ? [...specificNetworks, ...allSourceNetworks]
                : specificNetworks,
        });
    };

    return (
        <ContentCard noPadding>
            <div className='flex flex-col gap-3 border-b border-border px-5 py-4 sm:flex-row sm:items-center sm:justify-between'>
                <div className='flex items-center gap-2'>
                    <Network className='h-4 w-4 text-primary' />
                    <h2 className='text-sm font-semibold'>Client IP resolution</h2>
                </div>
                <ToggleSwitch
                    label='Trust upstream proxy chain'
                    isSelected={waf.trusted_proxy_chain}
                    onChange={(trusted_proxy_chain) => onChange({ ...waf, trusted_proxy_chain })}
                />
            </div>
            <div className='space-y-4 p-5'>
                <p className='text-sm text-muted'>
                    Enable this when the site runs behind another CDN or reverse proxy and this
                    service acts as the L2 CDN. X-Forwarded-For is used only when the direct TCP
                    peer matches a trusted network below.
                </p>
                {waf.trusted_proxy_chain && (
                    <div className='space-y-4'>
                        <div className='flex items-start justify-between gap-4 rounded-md border border-border px-4 py-3'>
                            <div className='min-w-0'>
                                <div className='text-sm font-medium'>Allow all source networks</div>
                                <p className='mt-1 text-xs text-muted'>
                                    Accept proxy headers from any TCP peer. Use this only when
                                    direct access to the CDN edge is blocked upstream.
                                </p>
                            </div>
                            <ToggleSwitch
                                label='Allow all source networks'
                                isSelected={trustsAllSources}
                                onChange={setTrustsAllSources}
                            />
                        </div>
                        {!trustsAllSources && (
                            <FormField
                                required
                                error={
                                    !hasTrustedProxy
                                        ? 'Add at least one trusted proxy IP or CIDR.'
                                        : undefined
                                }
                                hint='One IPv4/IPv6 address or CIDR per line. Include every trusted upstream hop that may connect to this site.'
                                label='Trusted proxy networks'
                            >
                                <TextArea
                                    aria-label='Trusted proxy networks'
                                    placeholder={'203.0.113.0/24\n2001:db8:1234::/48'}
                                    rows={3}
                                    value={waf.trusted_proxies.join('\n')}
                                    variant='secondary'
                                    onChange={(event) =>
                                        onChange({
                                            ...waf,
                                            trusted_proxies: event.target.value
                                                .split(/[\n,]/)
                                                .map((value) => value.trim()),
                                        })
                                    }
                                />
                            </FormField>
                        )}
                    </div>
                )}
            </div>
        </ContentCard>
    );
}
