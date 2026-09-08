import type { ReactNode } from 'react';
import type { DistributionItem } from '@/api';
import type { DonutSlice } from '@/components/DonutChart.tsx';

import {
    Check,
    FileText,
    HardDrive,
    Network,
    ScrollText,
    Server,
    Settings,
    Terminal,
} from 'lucide-react';

export type DetailTab = 'overview' | 'details' | 'logs' | 'installation' | 'settings';
export type SettingsPage = 'network' | 'cache';

export const tabs: Array<{ id: DetailTab; label: string; icon: typeof Server }> = [
    { id: 'overview', label: 'Overview', icon: Server },
    { id: 'details', label: 'Node details', icon: FileText },
    { id: 'logs', label: 'Runtime logs', icon: ScrollText },
    { id: 'installation', label: 'Installation', icon: Terminal },
    { id: 'settings', label: 'Settings', icon: Settings },
];
export function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const unit = Math.min(
        Math.max(0, Math.floor(Math.log(bytes) / Math.log(1024))),
        units.length - 1
    );
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

export function formatRate(bytesPerSecond: number) {
    return `${formatBytes(bytesPerSecond)}/s`;
}

export function InstallationProgress({ status }: { status: string }) {
    const steps = ['Queued', 'Installing', 'Health check', 'Online'];
    const stepIndex =
        status === 'PENDING'
            ? 0
            : status === 'INSTALLING'
              ? 1
              : status === 'OFFLINE'
                ? 2
                : status === 'ONLINE'
                  ? 3
                  : -1;
    return (
        <div className='grid gap-2 sm:grid-cols-4'>
            {steps.map((step, index) => {
                const complete = stepIndex > index || status === 'ONLINE';
                const active = stepIndex === index;
                return (
                    <div
                        className={`rounded-xl border px-3 py-3 ${
                            complete
                                ? 'border-success/25 bg-success/10'
                                : active
                                  ? 'border-primary/30 bg-primary/10'
                                  : 'border-border bg-surface-secondary/25'
                        }`}
                        key={step}
                    >
                        <div className='flex items-center gap-2'>
                            <span
                                className={`flex h-6 w-6 items-center justify-center rounded-full text-xs font-semibold ${
                                    complete
                                        ? 'bg-success text-success-foreground'
                                        : active
                                          ? 'bg-primary text-primary-foreground'
                                          : 'bg-surface-secondary text-muted'
                                }`}
                            >
                                {complete ? <Check className='h-3.5 w-3.5' /> : index + 1}
                            </span>
                            <span className='text-xs font-medium'>{step}</span>
                        </div>
                    </div>
                );
            })}
        </div>
    );
}

export function SectionTitle({
    icon: Icon,
    title,
    description,
}: {
    icon: typeof Server;
    title: string;
    description: string;
}) {
    return (
        <div className='flex items-start gap-3 border-b border-border px-5 py-4'>
            <span className='flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-surface-secondary text-muted'>
                <Icon className='h-4 w-4' />
            </span>
            <div>
                <h2 className='text-sm font-semibold'>{title}</h2>
                <p className='mt-0.5 text-xs leading-5 text-muted'>{description}</p>
            </div>
        </div>
    );
}

export function StatCell({
    label,
    value,
    footer,
    tone = 'default',
}: {
    label: string;
    value: ReactNode;
    footer?: ReactNode;
    tone?: 'default' | 'success' | 'danger';
}) {
    const toneClass = tone === 'success' ? 'text-success' : tone === 'danger' ? 'text-danger' : '';
    return (
        <div className='min-w-0 rounded-xl border border-border/70 bg-surface p-3.5 shadow-sm'>
            <div className='text-xs font-medium text-muted'>{label}</div>
            <div className={`mt-1 text-lg font-semibold tracking-tight ${toneClass}`}>{value}</div>
            {footer && <div className='mt-1 truncate text-xs text-muted'>{footer}</div>}
        </div>
    );
}

export function NameChips({ names, empty }: { names: string[]; empty: string }) {
    if (names.length === 0) return <span className='text-sm text-muted'>{empty}</span>;
    return (
        <div className='flex flex-wrap gap-2'>
            {names.map((name) => (
                <span
                    className='rounded-full border border-border bg-surface-secondary px-2.5 py-1 text-xs font-medium'
                    key={name}
                >
                    {name}
                </span>
            ))}
        </div>
    );
}

export function SettingsNavigation({
    page,
    onChange,
}: {
    page: SettingsPage;
    onChange: (page: SettingsPage) => void;
}) {
    return (
        <nav className='flex flex-col gap-1'>
            {[
                { id: 'network' as const, label: 'Network & DNS', icon: Network },
                { id: 'cache' as const, label: 'Cache', icon: HardDrive },
            ].map((item) => {
                const Icon = item.icon;
                return (
                    <button
                        className={`flex items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium transition-colors ${
                            page === item.id
                                ? 'bg-surface-secondary text-foreground'
                                : 'text-muted hover:bg-surface-secondary hover:text-foreground'
                        }`}
                        key={item.id}
                        type='button'
                        onClick={() => onChange(item.id)}
                    >
                        <Icon className='h-4 w-4' />
                        {item.label}
                    </button>
                );
            })}
        </nav>
    );
}

export const slicePalette = [
    '#3b82f6',
    '#10b981',
    '#f59e0b',
    '#8b5cf6',
    '#06b6d4',
    '#ec4899',
    '#84cc16',
    '#f97316',
    '#64748b',
];

export const methodColors: Record<string, string> = {
    GET: '#3b82f6',
    POST: '#10b981',
    PUT: '#f59e0b',
    DELETE: '#ef4444',
    PATCH: '#8b5cf6',
    HEAD: '#64748b',
    OPTIONS: '#06b6d4',
};

export function statusColor(value: string) {
    if (value.startsWith('2')) return '#10b981';
    if (value.startsWith('3')) return '#3b82f6';
    if (value.startsWith('4')) return '#f59e0b';
    if (value.startsWith('5')) return '#ef4444';
    return '#64748b';
}

export function toSlices(
    items: DistributionItem[],
    colorAt: (value: string, index: number) => string,
    maxSlices = 6
): DonutSlice[] {
    const slices = items.map((item, index) => ({
        label: item.value || '(empty)',
        value: item.requests,
        color: colorAt(item.value, index),
    }));
    if (slices.length <= maxSlices + 1) return slices;
    const rest = slices.slice(maxSlices);
    return [
        ...slices.slice(0, maxSlices),
        {
            label: 'Other',
            value: rest.reduce((sum, slice) => sum + slice.value, 0),
            color: '#94a3b8',
        },
    ];
}
