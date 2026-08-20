import assert from 'node:assert/strict';
import test from 'node:test';

import {
    alertStatusLabels,
    formatAlertAge,
    formatRuleSeconds,
    humanizeAlertKind,
    severityBadgeClass,
    severityLabel,
} from './alerts.ts';

test('alert kinds humanize to readable labels', () => {
    assert.equal(humanizeAlertKind('node_offline'), 'Node offline');
    assert.equal(humanizeAlertKind('origin_error_rate'), 'Origin error rate');
    assert.equal(humanizeAlertKind('cert_expiring'), 'Cert expiring');
});

test('severity helpers map known values and fall back gracefully', () => {
    assert.equal(severityLabel('CRITICAL'), 'Critical');
    assert.equal(severityLabel('warning'), 'Warning');
    assert.equal(severityLabel(''), '');
    assert.equal(severityBadgeClass('CRITICAL'), 'bg-danger/15 text-danger');
    assert.equal(severityBadgeClass('UNKNOWN'), 'bg-default text-muted');
});

test('alert ages render relative buckets', () => {
    const now = Date.parse('2026-08-10T12:00:00Z');
    assert.equal(formatAlertAge(new Date(now - 30_000).toISOString(), now), 'just now');
    assert.equal(formatAlertAge(new Date(now - 5 * 60_000).toISOString(), now), '5m ago');
    assert.equal(formatAlertAge(new Date(now - 3 * 3_600_000).toISOString(), now), '3h ago');
    assert.equal(formatAlertAge(new Date(now - 26 * 3_600_000).toISOString(), now), '1d ago');
    assert.equal(formatAlertAge(undefined, now), '');
    assert.equal(formatAlertAge(new Date(now + 60_000).toISOString(), now), '');
});

test('rule timers format compactly', () => {
    assert.equal(formatRuleSeconds(0), 'immediate');
    assert.equal(formatRuleSeconds(300), '5m');
    assert.equal(formatRuleSeconds(3600), '1h');
    assert.equal(formatRuleSeconds(21600), '6h');
    assert.equal(formatRuleSeconds(45), '45s');
    assert.equal(formatRuleSeconds(-1), 'immediate');
});

test('alert status labels cover the full state machine', () => {
    assert.deepEqual(Object.keys(alertStatusLabels).sort(), [
        'ACKNOWLEDGED',
        'FIRING',
        'PENDING',
        'RESOLVED',
    ]);
    assert.equal(alertStatusLabels.FIRING, 'Firing');
});
