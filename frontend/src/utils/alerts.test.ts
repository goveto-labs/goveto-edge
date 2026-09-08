import { expect, test } from 'vitest';

import {
    alertStatusLabels,
    formatAlertAge,
    formatRuleSeconds,
    humanizeAlertKind,
    severityBadgeClass,
    severityLabel,
} from './alerts.ts';

test('alert kinds humanize to readable labels', () => {
    expect(humanizeAlertKind('node_offline')).toBe('Node offline');
    expect(humanizeAlertKind('origin_error_rate')).toBe('Origin error rate');
    expect(humanizeAlertKind('cert_expiring')).toBe('Cert expiring');
});

test('severity helpers map known values and fall back gracefully', () => {
    expect(severityLabel('CRITICAL')).toBe('Critical');
    expect(severityLabel('warning')).toBe('Warning');
    expect(severityLabel('')).toBe('');
    expect(severityBadgeClass('CRITICAL')).toBe('bg-danger/15 text-danger');
    expect(severityBadgeClass('UNKNOWN')).toBe('bg-default text-muted');
});

test('alert ages render relative buckets', () => {
    const now = Date.parse('2026-08-10T12:00:00Z');
    expect(formatAlertAge(new Date(now - 30_000).toISOString(), now)).toBe('just now');
    expect(formatAlertAge(new Date(now - 5 * 60_000).toISOString(), now)).toBe('5m ago');
    expect(formatAlertAge(new Date(now - 3 * 3_600_000).toISOString(), now)).toBe('3h ago');
    expect(formatAlertAge(new Date(now - 26 * 3_600_000).toISOString(), now)).toBe('1d ago');
    expect(formatAlertAge(undefined, now)).toBe('');
    expect(formatAlertAge(new Date(now + 60_000).toISOString(), now)).toBe('');
});

test('rule timers format compactly', () => {
    expect(formatRuleSeconds(0)).toBe('immediate');
    expect(formatRuleSeconds(300)).toBe('5m');
    expect(formatRuleSeconds(3600)).toBe('1h');
    expect(formatRuleSeconds(21600)).toBe('6h');
    expect(formatRuleSeconds(45)).toBe('45s');
    expect(formatRuleSeconds(-1)).toBe('immediate');
});

test('alert status labels cover the full state machine', () => {
    expect(Object.keys(alertStatusLabels).sort()).toEqual([
        'ACKNOWLEDGED',
        'FIRING',
        'PENDING',
        'RESOLVED',
    ]);
    expect(alertStatusLabels.FIRING).toBe('Firing');
});
