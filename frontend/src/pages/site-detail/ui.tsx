import type { ReactNode } from 'react';
import type { DistributionItem, SecurityPolicy, SiteOrigin } from '@/api';
import type { DonutSlice } from '@/components/DonutChart.tsx';

import {
    BarChart3,
    FileArchive,
    FileText,
    Globe2,
    HardDrive,
    LockKeyhole,
    Route,
    ScrollText,
    Server,
    Settings,
    ShieldCheck,
    UsersRound,
} from 'lucide-react';

import { ApiError } from '@/api';
import { ContentCard } from '@/components/ContentCard.tsx';
import { chartColors } from '@/components/chartColors.ts';
import { countryOptions } from '@/data/countries.ts';

export type DetailTab = 'overview' | 'audience' | 'logs' | 'settings';
export type SettingsPage =
    | 'basic'
    | 'domains'
    | 'http'
    | 'https'
    | 'origins'
    | 'delivery'
    | 'security'
    | 'cache'
    | 'cache-rules'
    | 'compression';
export type Period = '24h' | '30d';
export type OriginDraft = SiteOrigin & { draft_id: string };

export function withSecurityEditorIDs(policy: SecurityPolicy): SecurityPolicy {
    return {
        waf: {
            ...policy.waf,
            trusted_proxy_chain: policy.waf.trusted_proxy_chain ?? false,
            trusted_proxies: policy.waf.trusted_proxies ?? [],
            rule_sets: (policy.waf.rule_sets ?? []).map((ruleSet) => ({
                ...ruleSet,
                rules: (ruleSet.rules ?? []).map((rule) => ({
                    ...rule,
                    id: rule.id ?? crypto.randomUUID(),
                    conditions: {
                        operator: rule.conditions?.operator ?? 'AND',
                        groups: (rule.conditions?.groups ?? []).map((group) => ({
                            id: group.id ?? crypto.randomUUID(),
                            operator: group.operator ?? 'AND',
                            conditions: (group.conditions ?? []).map((condition) => ({
                                ...condition,
                                id: condition.id ?? crypto.randomUUID(),
                            })),
                        })),
                    },
                    action: {
                        ...rule.action,
                        response: rule.action.response ?? { type: 'DEFAULT' },
                    },
                })),
            })),
        },
    };
}

export function loadErrorMessage(error: unknown) {
    return error instanceof ApiError ? error.message : 'request failed';
}

export function valuesEqual(left: unknown, right: unknown) {
    return JSON.stringify(left) === JSON.stringify(right);
}

export function setsEqual(left: Set<string>, right: Set<string>) {
    return left.size === right.size && Array.from(left).every((value) => right.has(value));
}

export function originsEqual(drafts: OriginDraft[], saved: SiteOrigin[]) {
    return valuesEqual(
        drafts.map(({ draft_id: _draftID, ...origin }) => origin),
        saved
    );
}

export const tabs = [
    { id: 'overview' as const, label: 'Overview', icon: BarChart3 },
    { id: 'audience' as const, label: 'Audience', icon: UsersRound },
    { id: 'logs' as const, label: 'Logs', icon: ScrollText },
    { id: 'settings' as const, label: 'Settings', icon: Settings },
];
export const settingsPages = [
    { id: 'basic' as const, label: 'Basic', icon: Settings },
    { id: 'domains' as const, label: 'Domains', icon: Globe2 },
    { id: 'http' as const, label: 'HTTP', icon: FileText },
    { id: 'https' as const, label: 'HTTPS', icon: LockKeyhole },
    { id: 'origins' as const, label: 'Origins', icon: Server },
    { id: 'delivery' as const, label: 'Delivery', icon: Route },
    { id: 'security' as const, label: 'Security', icon: ShieldCheck },
    { id: 'cache' as const, label: 'Cache', icon: HardDrive },
    { id: 'cache-rules' as const, label: 'Rules', icon: HardDrive },
    { id: 'compression' as const, label: 'Compression', icon: FileArchive },
];
export type SettingsNavEntry =
    | { kind: 'page'; id: SettingsPage }
    | { kind: 'divider' }
    | { kind: 'group'; id: SettingsPage; children: { id: SettingsPage; label: string }[] };
export const settingsNav: SettingsNavEntry[] = [
    { kind: 'page', id: 'basic' },
    { kind: 'page', id: 'domains' },
    { kind: 'page', id: 'http' },
    { kind: 'page', id: 'https' },
    { kind: 'page', id: 'origins' },
    { kind: 'divider' },
    { kind: 'page', id: 'delivery' },
    { kind: 'page', id: 'security' },
    {
        kind: 'group',
        id: 'cache',
        children: [
            { id: 'cache', label: 'Settings' },
            { id: 'cache-rules', label: 'Rules' },
        ],
    },
    { kind: 'page', id: 'compression' },
];
export const palette = [
    chartColors.primary,
    chartColors.secondary,
    chartColors.tertiary,
    chartColors.warning,
    chartColors.danger,
    chartColors.neutral,
];
export const countryNames = new Map(countryOptions.map((country) => [country.id, country.name]));

export function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const unit = Math.max(
        0,
        Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
    );
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

export function formatBandwidth(bytesPerSecond: number) {
    const bits = bytesPerSecond * 8;
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
    if (bits <= 0) return '0 bps';
    const unit = Math.max(
        0,
        Math.min(Math.floor(Math.log(bits) / Math.log(1000)), units.length - 1)
    );
    return `${(bits / 1000 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

export function trafficOf(item: { ingress_bytes: number; egress_bytes: number }) {
    return item.ingress_bytes + item.egress_bytes;
}

export function toSlices(items: DistributionItem[], colorOffset = 0): DonutSlice[] {
    return items.slice(0, 6).map((item, index) => ({
        label: item.value || '(empty)',
        value: item.requests,
        color: palette[(index + colorOffset) % palette.length],
    }));
}

export function SectionHeader({ title, description }: { title: ReactNode; description: string }) {
    return (
        <div className='border-b border-border px-5 py-4'>
            <h2 className='text-sm font-semibold'>{title}</h2>
            <p className='mt-1 text-xs leading-5 text-muted'>{description}</p>
        </div>
    );
}

export function Metric({ label, value, note }: { label: string; value: string; note?: string }) {
    return (
        <div className='min-w-0 border-b border-border px-4 py-4 last:border-b-0 sm:border-b-0 sm:border-r sm:last:border-r-0'>
            <div className='text-xs font-medium text-muted'>{label}</div>
            <div className='tabular mt-1 font-mono text-xl font-semibold tracking-tight'>
                {value}
            </div>
            {note && <div className='mt-1 truncate text-xs text-muted'>{note}</div>}
        </div>
    );
}

export function RankingTable({
    title,
    label,
    items,
    description,
    limit = 10,
    formatValue = (value) => value || '(empty)',
    scrollable = false,
}: {
    title: ReactNode;
    label: string;
    items: DistributionItem[];
    description?: string;
    limit?: number;
    formatValue?: (value: string) => string;
    scrollable?: boolean;
}) {
    return (
        <ContentCard className='h-full' noPadding>
            <SectionHeader
                title={title}
                description={description ?? `Top ${label.toLowerCase()} for the selected period.`}
            />
            <div className={scrollable ? 'max-h-[520px] overflow-auto' : 'overflow-x-auto'}>
                <table className='w-full min-w-[520px] text-left text-sm'>
                    <thead className='sticky top-0 z-10 bg-surface-secondary text-xs text-muted'>
                        <tr>
                            <th className='px-4 py-2.5'>{label}</th>
                            <th className='px-4 py-2.5 text-right'>Requests</th>
                            <th className='px-4 py-2.5 text-right'>Traffic</th>
                        </tr>
                    </thead>
                    <tbody className='divide-y divide-border'>
                        {items.slice(0, limit).map((item) => (
                            <tr key={item.value}>
                                <td
                                    className='max-w-sm truncate px-4 py-2.5 font-mono text-xs'
                                    title={item.value}
                                >
                                    {formatValue(item.value)}
                                </td>
                                <td className='tabular px-4 py-2.5 text-right font-mono'>
                                    {item.requests.toLocaleString()}
                                </td>
                                <td className='tabular px-4 py-2.5 text-right text-muted'>
                                    {formatBytes(trafficOf(item))}
                                </td>
                            </tr>
                        ))}
                        {items.length === 0 && (
                            <tr>
                                <td className='px-4 py-10 text-center text-muted' colSpan={3}>
                                    No analytics data
                                </td>
                            </tr>
                        )}
                    </tbody>
                </table>
            </div>
        </ContentCard>
    );
}
