import type { CompressionPolicy } from '@/api';

import { Button, Input, TextArea } from '@heroui/react';
import { FileArchive, FileType2, Gauge, Plus, RouteOff, Save, Settings2, X } from 'lucide-react';
import { useState } from 'react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { FormField } from '@/components/FormField.tsx';
import { SelectField } from '@/components/SelectField.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

interface SiteCompressionSettingsProps {
    compression: CompressionPolicy;
    saving: boolean;
    onChange: (compression: CompressionPolicy) => void;
    onSave: () => void;
}

const defaultExtensions = [
    'css',
    'csv',
    'htm',
    'html',
    'js',
    'json',
    'map',
    'md',
    'mjs',
    'svg',
    'txt',
    'wasm',
    'xml',
];
const defaultExcludedExtensions = [
    '7z',
    'avif',
    'br',
    'bz2',
    'gif',
    'gz',
    'jpeg',
    'jpg',
    'mov',
    'mp3',
    'mp4',
    'pdf',
    'png',
    'rar',
    'webm',
    'webp',
    'zip',
    'zst',
];
const defaultMIMETypes = [
    'application/javascript',
    'application/json',
    'application/manifest+json',
    'application/wasm',
    'application/xml',
    'image/svg+xml',
    'text/*',
];

type SizeUnit = 'B' | 'KB' | 'MB';

const sizeUnitFactors: Record<SizeUnit, number> = {
    B: 1,
    KB: 1024,
    MB: 1024 * 1024,
};

function splitList(value: string) {
    return value
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean);
}

function BadgeListEditor({
    id,
    values,
    placeholder,
    addLabel,
    formatValue = (value) => value,
    normalizeValue = (value) => value.trim(),
    onChange,
}: {
    id: string;
    values: string[];
    placeholder: string;
    addLabel: string;
    formatValue?: (value: string) => string;
    normalizeValue?: (value: string) => string;
    onChange: (values: string[]) => void;
}) {
    const [draft, setDraft] = useState('');
    const addValues = () => {
        const additions = splitList(draft)
            .map(normalizeValue)
            .filter(Boolean)
            .filter(
                (value, index, items) =>
                    items.findIndex((item) => item.toLowerCase() === value.toLowerCase()) === index
            );
        if (additions.length === 0) return;
        const existing = new Set(values.map((value) => value.toLowerCase()));
        onChange([...values, ...additions.filter((value) => !existing.has(value.toLowerCase()))]);
        setDraft('');
    };

    return (
        <div className='space-y-3 rounded-xl border border-border/70 bg-surface-secondary/15 p-3'>
            <div className='flex min-h-8 flex-wrap gap-2'>
                {values.map((value) => (
                    <span
                        className='inline-flex items-center gap-1.5 rounded-full border border-border bg-surface px-3 py-1 text-xs font-medium'
                        key={value}
                    >
                        {formatValue(value)}
                        <button
                            aria-label={`Remove ${value}`}
                            className='rounded-full text-muted transition-colors hover:text-foreground'
                            type='button'
                            onClick={() => onChange(values.filter((item) => item !== value))}
                        >
                            <X className='h-3.5 w-3.5' />
                        </button>
                    </span>
                ))}
            </div>
            <div className='flex flex-col gap-2 sm:flex-row'>
                <Input
                    id={id}
                    className='min-w-0 flex-1'
                    placeholder={placeholder}
                    value={draft}
                    variant='secondary'
                    onChange={(event) => setDraft(event.target.value)}
                    onKeyDown={(event) => {
                        if (event.key !== 'Enter') return;
                        event.preventDefault();
                        addValues();
                    }}
                />
                <Button
                    isDisabled={!draft.trim()}
                    type='button'
                    variant='secondary'
                    onPress={addValues}
                >
                    <Plus className='mr-1.5 h-4 w-4' />
                    {addLabel}
                </Button>
            </div>
        </div>
    );
}

function ContentLengthInput({
    id,
    bytes,
    defaultUnit,
    minimumBytes,
    maximumBytes,
    onChange,
}: {
    id: string;
    bytes: number;
    defaultUnit: SizeUnit;
    minimumBytes: number;
    maximumBytes: number;
    onChange: (bytes: number) => void;
}) {
    const [unit, setUnit] = useState<SizeUnit>(defaultUnit);
    const factor = sizeUnitFactors[unit];
    const value = bytes / factor;
    const displayValue = Number.isInteger(value) ? String(value) : String(Number(value.toFixed(3)));

    return (
        <div className='flex min-w-0 gap-2'>
            <Input
                id={id}
                className='min-w-0 flex-1'
                min={minimumBytes / factor}
                max={maximumBytes / factor}
                step={unit === 'B' ? 1 : 0.1}
                type='number'
                value={displayValue}
                variant='secondary'
                onChange={(event) =>
                    onChange(Math.round(Number(event.target.value) * sizeUnitFactors[unit]))
                }
            />
            <SelectField
                ariaLabel={`${id} unit`}
                className='w-24 shrink-0'
                options={[
                    { id: 'B', label: 'B' },
                    { id: 'KB', label: 'KB' },
                    { id: 'MB', label: 'MB' },
                ]}
                value={unit}
                variant='secondary'
                onChange={(value) => setUnit(value as SizeUnit)}
            />
        </div>
    );
}

function Section({
    icon: Icon,
    title,
    description,
    children,
}: {
    icon: typeof FileArchive;
    title: string;
    description: string;
    children: React.ReactNode;
}) {
    return (
        <section className='grid gap-5 border-t border-border px-5 py-6 lg:grid-cols-[220px_minmax(0,1fr)] lg:px-6'>
            <div>
                <div className='flex items-center gap-2.5'>
                    <span className='flex h-8 w-8 items-center justify-center rounded-lg bg-surface-secondary text-muted'>
                        <Icon className='h-4 w-4' />
                    </span>
                    <h3 className='text-sm font-semibold'>{title}</h3>
                </div>
                <p className='mt-2 max-w-[34ch] text-xs leading-5 text-muted'>{description}</p>
            </div>
            <div className='min-w-0'>{children}</div>
        </section>
    );
}

export function SiteCompressionSettings({
    compression,
    saving,
    onChange,
    onSave,
}: SiteCompressionSettingsProps) {
    const minimumLength = compression.minimum_length ?? 1024;
    const maximumLength = compression.maximum_length ?? 10_485_760;
    const extensions = compression.extensions ?? defaultExtensions;
    const excludedExtensions = compression.excluded_extensions ?? defaultExcludedExtensions;
    const mimeTypes = compression.mime_types ?? defaultMIMETypes;
    const limitsValid =
        minimumLength >= 0 &&
        maximumLength >= 1 &&
        maximumLength <= 67_108_864 &&
        minimumLength <= maximumLength;
    const matchersValid = extensions.length > 0 || mimeTypes.length > 0;

    return (
        <ContentCard className='overflow-hidden' noPadding>
            <div className='flex flex-col gap-5 px-5 py-5 sm:flex-row sm:items-center sm:justify-between lg:px-6'>
                <div className='flex items-center gap-3'>
                    <span
                        className={`flex h-10 w-10 items-center justify-center rounded-xl ${compression.enabled ? 'bg-primary text-primary-foreground' : 'bg-surface-secondary text-muted'}`}
                    >
                        <FileArchive className='h-5 w-5' />
                    </span>
                    <div>
                        <h2 className='text-base font-semibold'>Content compression</h2>
                        <p className='mt-0.5 text-xs text-muted'>
                            {compression.enabled
                                ? 'Compress matching responses at the edge.'
                                : 'Responses are passed through without edge compression.'}
                        </p>
                    </div>
                </div>
                <div className='flex items-center gap-3'>
                    <span
                        className={`text-xs font-medium ${compression.enabled ? 'text-success' : 'text-muted'}`}
                    >
                        {compression.enabled ? 'Enabled' : 'Disabled'}
                    </span>
                    <ToggleSwitch
                        label='Enable content compression'
                        isSelected={compression.enabled ?? false}
                        onChange={(enabled) =>
                            onChange({
                                ...compression,
                                enabled,
                                extensions: compression.extensions ?? [...defaultExtensions],
                                excluded_extensions: compression.excluded_extensions ?? [
                                    ...defaultExcludedExtensions,
                                ],
                                mime_types: compression.mime_types ?? [...defaultMIMETypes],
                                minimum_length: compression.minimum_length ?? 1024,
                                maximum_length: compression.maximum_length ?? 10_485_760,
                                excluded_paths: compression.excluded_paths ?? [],
                            })
                        }
                    />
                </div>
            </div>

            {compression.enabled && (
                <div className='border-t border-border'>
                    <Section
                        icon={FileType2}
                        title='Content matching'
                        description='A response is eligible when its extension or MIME type matches. Excluded extensions always win.'
                    >
                        <div className='grid gap-5'>
                            <FormField
                                htmlFor='compression-extensions'
                                label='Supported extensions'
                                hint='Add extensions that are suitable for compression. A leading dot is optional.'
                            >
                                <BadgeListEditor
                                    id='compression-extensions'
                                    addLabel='Add extension'
                                    formatValue={(value) => `.${value}`}
                                    normalizeValue={(value) =>
                                        value.trim().toLowerCase().replace(/^\./, '')
                                    }
                                    placeholder='e.g. html or css'
                                    values={extensions}
                                    onChange={(values) =>
                                        onChange({
                                            ...compression,
                                            extensions: values,
                                        })
                                    }
                                />
                            </FormField>
                            <FormField
                                htmlFor='compression-excluded-extensions'
                                label='Excluded extensions'
                                hint='Already compressed or binary formats that must never be compressed.'
                            >
                                <BadgeListEditor
                                    id='compression-excluded-extensions'
                                    addLabel='Add exception'
                                    formatValue={(value) => `.${value}`}
                                    normalizeValue={(value) =>
                                        value.trim().toLowerCase().replace(/^\./, '')
                                    }
                                    placeholder='e.g. jpg or zip'
                                    values={excludedExtensions}
                                    onChange={(values) =>
                                        onChange({
                                            ...compression,
                                            excluded_extensions: values,
                                        })
                                    }
                                />
                            </FormField>
                            <FormField
                                htmlFor='compression-mime-types'
                                label='Supported MIME types'
                                hint='Exact values and type wildcards such as text/* are supported.'
                                error={
                                    matchersValid
                                        ? undefined
                                        : 'Add at least one extension or MIME type.'
                                }
                            >
                                <BadgeListEditor
                                    id='compression-mime-types'
                                    addLabel='Add MIME type'
                                    normalizeValue={(value) => value.trim().toLowerCase()}
                                    placeholder='e.g. text/* or application/json'
                                    values={mimeTypes}
                                    onChange={(values) =>
                                        onChange({
                                            ...compression,
                                            mime_types: values,
                                        })
                                    }
                                />
                            </FormField>
                        </div>
                    </Section>

                    <Section
                        icon={Gauge}
                        title='Content length'
                        description='Only responses inside this inclusive byte range are compressed.'
                    >
                        <div className='grid gap-4 sm:grid-cols-2'>
                            <FormField
                                htmlFor='compression-minimum-length'
                                label='Minimum length'
                                hint='Small responses often cost more CPU than they save.'
                            >
                                <ContentLengthInput
                                    id='compression-minimum-length'
                                    bytes={minimumLength}
                                    defaultUnit='KB'
                                    minimumBytes={0}
                                    maximumBytes={67_108_864}
                                    onChange={(minimum_length) =>
                                        onChange({
                                            ...compression,
                                            minimum_length,
                                        })
                                    }
                                />
                            </FormField>
                            <FormField
                                htmlFor='compression-maximum-length'
                                label='Maximum length'
                                hint='Larger responses pass through without buffering.'
                                error={
                                    limitsValid
                                        ? undefined
                                        : 'Maximum must be at least the minimum and no more than 67108864.'
                                }
                            >
                                <ContentLengthInput
                                    id='compression-maximum-length'
                                    bytes={maximumLength}
                                    defaultUnit='MB'
                                    minimumBytes={1}
                                    maximumBytes={67_108_864}
                                    onChange={(maximum_length) =>
                                        onChange({
                                            ...compression,
                                            maximum_length,
                                        })
                                    }
                                />
                            </FormField>
                        </div>
                    </Section>

                    <Section
                        icon={RouteOff}
                        title='Excluded URLs'
                        description='Ignore exact paths and their descendants. Query strings are not considered.'
                    >
                        <FormField
                            htmlFor='compression-excluded-paths'
                            label='Ignored path prefixes'
                            hint='One path per line. Every value must start with /.'
                        >
                            <TextArea
                                id='compression-excluded-paths'
                                rows={4}
                                placeholder={'/downloads\n/api/export'}
                                value={(compression.excluded_paths ?? []).join('\n')}
                                variant='secondary'
                                onChange={(event) =>
                                    onChange({
                                        ...compression,
                                        excluded_paths: splitList(event.target.value),
                                    })
                                }
                            />
                        </FormField>
                    </Section>

                    <Section
                        icon={Settings2}
                        title='Advanced behavior'
                        description='Control how responses that already have a Content-Encoding header are handled.'
                    >
                        <div className='flex items-center justify-between gap-5 rounded-xl border border-border/70 bg-surface-secondary/20 px-4 py-3.5'>
                            <div>
                                <div className='text-sm font-medium'>
                                    Recompress encoded content
                                </div>
                                <div className='mt-0.5 text-xs leading-5 text-muted'>
                                    Decode supported origin encodings and apply a higher-priority
                                    algorithm accepted by the client.
                                </div>
                            </div>
                            <ToggleSwitch
                                label='Recompress encoded content'
                                isSelected={compression.recompress ?? false}
                                onChange={(recompress) => onChange({ ...compression, recompress })}
                            />
                        </div>
                    </Section>

                    <div className='flex justify-end border-t border-border px-5 py-4 lg:px-6'>
                        <Button
                            isDisabled={saving || !limitsValid || !matchersValid}
                            onPress={onSave}
                        >
                            <Save className='mr-1.5 h-4 w-4' />
                            Save compression settings
                        </Button>
                    </div>
                </div>
            )}

            {!compression.enabled && (
                <div className='flex justify-end border-t border-border px-5 py-4 lg:px-6'>
                    <Button isDisabled={saving} onPress={onSave}>
                        <Save className='mr-1.5 h-4 w-4' />
                        Save compression settings
                    </Button>
                </div>
            )}
        </ContentCard>
    );
}
