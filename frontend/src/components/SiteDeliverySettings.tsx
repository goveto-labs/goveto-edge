import type { DeliveryPolicy, HeaderRule } from '@/api';

import { Button, Input, TextArea } from '@heroui/react';
import { Plus, Save, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { FormField } from '@/components/FormField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';
import { normalizeDeliveryPolicy } from '@/utils/delivery.ts';

interface Props {
    policy: DeliveryPolicy;
    saving: boolean;
    onChange: (policy: DeliveryPolicy) => void;
    onSave: () => void;
}

function splitLines(value: string) {
    return value
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean);
}

function HeaderEditor({
    label,
    rules,
    onChange,
}: {
    label: string;
    rules: HeaderRule[];
    onChange: (rules: HeaderRule[]) => void;
}) {
    return (
        <div className='space-y-3'>
            <div className='flex items-center justify-between gap-3'>
                <h3 className='text-sm font-semibold'>{label}</h3>
                <Button
                    size='sm'
                    variant='secondary'
                    onPress={() => onChange([...rules, { operation: 'SET', name: '', value: '' }])}
                >
                    <Plus className='mr-1.5 h-3.5 w-3.5' /> Add header
                </Button>
            </div>
            {rules.length === 0 && <p className='text-sm text-muted'>No custom headers.</p>}
            {rules.map((rule, index) => (
                <div
                    className='grid gap-2 sm:grid-cols-[110px_1fr_1.4fr_36px]'
                    key={`${rule.operation}:${rule.name}:${rule.value || ''}`}
                >
                    <select
                        aria-label={`${label} operation ${index + 1}`}
                        className='h-10 rounded-lg border border-border bg-surface px-3 text-sm'
                        value={rule.operation}
                        onChange={(event) =>
                            onChange(
                                rules.map((item, itemIndex) =>
                                    itemIndex === index
                                        ? {
                                              ...item,
                                              operation: event.target
                                                  .value as HeaderRule['operation'],
                                          }
                                        : item
                                )
                            )
                        }
                    >
                        <option value='SET'>Set</option>
                        <option value='ADD'>Add</option>
                        <option value='DELETE'>Delete</option>
                    </select>
                    <Input
                        aria-label={`${label} name ${index + 1}`}
                        placeholder='X-Header-Name'
                        value={rule.name}
                        variant='secondary'
                        onChange={(event) =>
                            onChange(
                                rules.map((item, itemIndex) =>
                                    itemIndex === index
                                        ? { ...item, name: event.target.value }
                                        : item
                                )
                            )
                        }
                    />
                    <Input
                        aria-label={`${label} value ${index + 1}`}
                        disabled={rule.operation === 'DELETE'}
                        placeholder='Header value or Caddy placeholder'
                        value={rule.value || ''}
                        variant='secondary'
                        onChange={(event) =>
                            onChange(
                                rules.map((item, itemIndex) =>
                                    itemIndex === index
                                        ? { ...item, value: event.target.value }
                                        : item
                                )
                            )
                        }
                    />
                    <Button
                        isIconOnly
                        aria-label={`Remove ${label.toLowerCase()} ${index + 1}`}
                        variant='ghost'
                        onPress={() =>
                            onChange(rules.filter((_, itemIndex) => itemIndex !== index))
                        }
                    >
                        <Trash2 className='h-4 w-4 text-danger' />
                    </Button>
                </div>
            ))}
        </div>
    );
}

function JSONField({
    label,
    hint,
    value,
    onChange,
    onValidityChange,
}: {
    label: string;
    hint: string;
    value: unknown;
    onChange: (value: unknown) => void;
    onValidityChange: (valid: boolean) => void;
}) {
    const [text, setText] = useState(() => JSON.stringify(value, null, 2));
    const [error, setError] = useState('');
    useEffect(() => setText(JSON.stringify(value, null, 2)), [value]);
    return (
        <FormField error={error} hint={hint} label={label}>
            <TextArea
                className='font-mono text-xs'
                rows={8}
                value={text}
                variant='secondary'
                onChange={(event) => {
                    const next = event.target.value;
                    setText(next);
                    try {
                        onChange(JSON.parse(next));
                        setError('');
                        onValidityChange(true);
                    } catch {
                        setError('Enter valid JSON before saving.');
                        onValidityChange(false);
                    }
                }}
            />
        </FormField>
    );
}

export function SiteDeliverySettings({ policy: inputPolicy, saving, onChange, onSave }: Props) {
    const policy = normalizeDeliveryPolicy(inputPolicy);
    const [invalidJSONFields, setInvalidJSONFields] = useState<Set<string>>(new Set());
    const setJSONValidity = (field: string, valid: boolean) => {
        setInvalidJSONFields((current) => {
            const next = new Set(current);
            if (valid) next.delete(field);
            else next.add(field);
            return next;
        });
    };

    return (
        <div className='space-y-4'>
            <ContentCard title='Availability and upstream path'>
                <div className='grid gap-5 md:grid-cols-2'>
                    <div className='space-y-3'>
                        <ToggleSwitch
                            isSelected={policy.maintenance.enabled}
                            label='Maintenance mode'
                            onChange={(enabled) =>
                                onChange({
                                    ...policy,
                                    maintenance: { ...policy.maintenance, enabled },
                                })
                            }
                        />
                        <FormField label='Maintenance response'>
                            <TextArea
                                rows={4}
                                value={policy.maintenance.body}
                                variant='secondary'
                                onChange={(event) =>
                                    onChange({
                                        ...policy,
                                        maintenance: {
                                            ...policy.maintenance,
                                            body: event.target.value,
                                        },
                                    })
                                }
                            />
                        </FormField>
                    </div>
                    <FormField
                        hint='Prepended only to the URI sent upstream. Leave empty to preserve the request path.'
                        label='Origin path prefix'
                    >
                        <Input
                            placeholder='/production'
                            value={policy.origin_prefix}
                            variant='secondary'
                            onChange={(event) =>
                                onChange({ ...policy, origin_prefix: event.target.value })
                            }
                        />
                    </FormField>
                </div>
            </ContentCard>

            <ContentCard title='Request and response headers'>
                <div className='space-y-6'>
                    <HeaderEditor
                        label='Request headers'
                        rules={policy.request_headers}
                        onChange={(request_headers) => onChange({ ...policy, request_headers })}
                    />
                    <HeaderEditor
                        label='Response headers'
                        rules={policy.response_headers}
                        onChange={(response_headers) => onChange({ ...policy, response_headers })}
                    />
                </div>
            </ContentCard>

            <ContentCard title='URL rules'>
                <div className='grid gap-6 xl:grid-cols-2'>
                    <JSONField
                        hint='Path matchers support Caddy wildcards. Replacement must be an absolute path.'
                        label='Rewrites'
                        value={policy.rewrites}
                        onValidityChange={(valid) => setJSONValidity('rewrites', valid)}
                        onChange={(rewrites) =>
                            onChange({
                                ...policy,
                                rewrites: rewrites as DeliveryPolicy['rewrites'],
                            })
                        }
                    />
                    <JSONField
                        hint='Use status 301, 302, 307, or 308. Location may be a URL or absolute path.'
                        label='Redirects'
                        value={policy.redirects}
                        onValidityChange={(valid) => setJSONValidity('redirects', valid)}
                        onChange={(redirects) =>
                            onChange({
                                ...policy,
                                redirects: redirects as DeliveryPolicy['redirects'],
                            })
                        }
                    />
                </div>
            </ContentCard>

            <ContentCard title='CORS and upgraded protocols'>
                <div className='grid gap-6 lg:grid-cols-2'>
                    <div className='space-y-4'>
                        <ToggleSwitch
                            isSelected={policy.cors.enabled}
                            label='Enable CORS'
                            onChange={(enabled) =>
                                onChange({ ...policy, cors: { ...policy.cors, enabled } })
                            }
                        />
                        <FormField label='Allowed origins'>
                            <TextArea
                                disabled={!policy.cors.enabled}
                                placeholder={'https://app.example.com\nhttps://admin.example.com'}
                                rows={3}
                                value={policy.cors.allow_origins.join('\n')}
                                variant='secondary'
                                onChange={(event) =>
                                    onChange({
                                        ...policy,
                                        cors: {
                                            ...policy.cors,
                                            allow_origins: splitLines(event.target.value),
                                        },
                                    })
                                }
                            />
                        </FormField>
                        <ToggleSwitch
                            isDisabled={!policy.cors.enabled}
                            isSelected={policy.cors.allow_credentials}
                            label='Allow credentials'
                            onChange={(allow_credentials) =>
                                onChange({ ...policy, cors: { ...policy.cors, allow_credentials } })
                            }
                        />
                    </div>
                    <div className='space-y-3'>
                        <ToggleSwitch
                            isSelected={policy.protocols.websocket}
                            label='WebSocket'
                            onChange={(websocket) =>
                                onChange({
                                    ...policy,
                                    protocols: { ...policy.protocols, websocket },
                                })
                            }
                        />
                        <ToggleSwitch
                            isSelected={policy.protocols.grpc}
                            label='gRPC and h2c upstream'
                            onChange={(grpc) =>
                                onChange({ ...policy, protocols: { ...policy.protocols, grpc } })
                            }
                        />
                        <ToggleSwitch
                            isSelected={policy.protocols.http_upgrade}
                            label='Other HTTP Upgrade protocols'
                            onChange={(http_upgrade) =>
                                onChange({
                                    ...policy,
                                    protocols: { ...policy.protocols, http_upgrade },
                                })
                            }
                        />
                    </div>
                </div>
            </ContentCard>

            <ContentCard title='Advanced routing'>
                <div className='space-y-6'>
                    <JSONField
                        hint='Each pool defines path matchers, scheduler, and its own origins.'
                        label='Path origin pools'
                        value={policy.origin_pools}
                        onValidityChange={(valid) => setJSONValidity('origin_pools', valid)}
                        onChange={(origin_pools) =>
                            onChange({
                                ...policy,
                                origin_pools: origin_pools as DeliveryPolicy['origin_pools'],
                            })
                        }
                    />
                    <JSONField
                        hint='Select a pool by request Header, Cookie, stable percentage, or a combination.'
                        label='A/B and request splits'
                        value={policy.splits}
                        onValidityChange={(valid) => setJSONValidity('splits', valid)}
                        onChange={(splits) =>
                            onChange({ ...policy, splits: splits as DeliveryPolicy['splits'] })
                        }
                    />
                    <JSONField
                        hint='Map one or more 4xx/5xx status codes to a custom body and content type.'
                        label='Custom error pages'
                        value={policy.error_pages}
                        onValidityChange={(valid) => setJSONValidity('error_pages', valid)}
                        onChange={(error_pages) =>
                            onChange({
                                ...policy,
                                error_pages: error_pages as DeliveryPolicy['error_pages'],
                            })
                        }
                    />
                </div>
            </ContentCard>

            <div className='flex justify-end'>
                <Button isDisabled={saving || invalidJSONFields.size > 0} onPress={onSave}>
                    <Save className='mr-1.5 h-4 w-4' /> Save delivery settings
                </Button>
            </div>
        </div>
    );
}
