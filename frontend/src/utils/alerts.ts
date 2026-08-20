/** Presentation helpers shared by the alert center, rules panel and bell. */

import type { AlertSeverity } from '@/api';

export type { AlertSeverity } from '@/api';

const severityStyles: Record<AlertSeverity, string> = {
    CRITICAL: 'bg-danger/15 text-danger',
    WARNING: 'bg-warning/15 text-warning',
    INFO: 'bg-primary/15 text-primary',
};

export function severityBadgeClass(severity: string): string {
    return severityStyles[severity as AlertSeverity] ?? 'bg-default text-muted';
}

export function severityDotClass(severity: string): string {
    if (severity === 'CRITICAL') return 'bg-danger';
    if (severity === 'WARNING') return 'bg-warning';
    return 'bg-primary';
}

export function severityLabel(severity: string): string {
    if (!severity) return '';
    const normalized = severity.toUpperCase();
    return normalized.charAt(0) + normalized.slice(1).toLowerCase();
}

export function humanizeAlertKind(kind: string): string {
    return kind
        .toLowerCase()
        .replace(/_/g, ' ')
        .replace(/^./, (letter) => letter.toUpperCase());
}

export const alertStatusLabels: Record<string, string> = {
    PENDING: 'Pending',
    FIRING: 'Firing',
    ACKNOWLEDGED: 'Acknowledged',
    RESOLVED: 'Resolved',
};

/** Relative age like "5m ago"; empty string for missing or future values. */
export function formatAlertAge(value?: string | null, now = Date.now()): string {
    if (!value) return '';
    const milliseconds = now - new Date(value).getTime();
    if (!Number.isFinite(milliseconds) || milliseconds < 0) return '';
    const minutes = Math.floor(milliseconds / 60_000);
    if (minutes < 1) return 'just now';
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    return `${Math.floor(hours / 24)}d ago`;
}

/** Compact duration like "immediate", "5m", "2h" for rule timers. */
export function formatRuleSeconds(seconds: number): string {
    if (!Number.isFinite(seconds) || seconds <= 0) return 'immediate';
    if (seconds % 3600 === 0) return `${seconds / 3600}h`;
    if (seconds % 60 === 0) return `${seconds / 60}m`;
    return `${seconds}s`;
}

/** Rule parameter keys like "window_minutes" render as "Window minutes". */
export function paramLabel(key: string): string {
    return key.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase());
}
