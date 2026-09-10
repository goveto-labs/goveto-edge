type Status = 'online' | 'offline' | 'pending' | 'failed' | 'completed' | 'disabled' | string;

interface StatusBadgeProps {
    status: Status;
}

const statusMap: Record<string, { className: string; label: string }> = {
    ONLINE: { className: 'bg-success-soft text-success-soft-foreground', label: 'Online' },
    OFFLINE: { className: 'bg-danger-soft text-danger-soft-foreground', label: 'Offline' },
    DISABLED: { className: 'bg-default text-muted', label: 'Disabled' },
    INSTALLING: { className: 'bg-warning-soft text-warning-soft-foreground', label: 'Installing' },
    INSTALL_FAILED: {
        className: 'bg-danger-soft text-danger-soft-foreground',
        label: 'Install failed',
    },
    PENDING: { className: 'bg-warning-soft text-warning-soft-foreground', label: 'Pending' },
    FIRING: { className: 'bg-danger-soft text-danger-soft-foreground', label: 'Firing' },
    ACKNOWLEDGED: {
        className: 'bg-accent-soft text-accent-soft-foreground',
        label: 'Acknowledged',
    },
    RESOLVED: { className: 'bg-success-soft text-success-soft-foreground', label: 'Resolved' },
    SENT: { className: 'bg-success-soft text-success-soft-foreground', label: 'Sent' },
    RUNNING: { className: 'bg-warning-soft text-warning-soft-foreground', label: 'Running' },
    COMPLETED: { className: 'bg-success-soft text-success-soft-foreground', label: 'Completed' },
    FAILED: { className: 'bg-danger-soft text-danger-soft-foreground', label: 'Failed' },
    DEAD_LETTER: { className: 'bg-danger-soft text-danger-soft-foreground', label: 'Dead letter' },
    CANCELLED: { className: 'bg-default text-muted', label: 'Cancelled' },
    SUCCEEDED: { className: 'bg-success-soft text-success-soft-foreground', label: 'Succeeded' },
    ACTIVE: { className: 'bg-success-soft text-success-soft-foreground', label: 'Active' },
    DEPLOYING: {
        className: 'bg-warning-soft text-warning-soft-foreground',
        label: 'Not active yet',
    },
    EXPIRING: {
        className: 'bg-warning-soft text-warning-soft-foreground',
        label: 'Expiring soon',
    },
    EXPIRED: { className: 'bg-danger-soft text-danger-soft-foreground', label: 'Expired' },
    RENEWAL_FAILED: {
        className: 'bg-danger-soft text-danger-soft-foreground',
        label: 'Renewal failed',
    },
    DEPLOYMENT_FAILED: {
        className: 'bg-danger-soft text-danger-soft-foreground',
        label: 'Publish failed',
    },
    REVOKING: { className: 'bg-warning-soft text-warning-soft-foreground', label: 'Revoking' },
    REVOKED: { className: 'bg-default text-muted', label: 'Revoked' },
    REVOCATION_FAILED: {
        className: 'bg-danger-soft text-danger-soft-foreground',
        label: 'Revocation failed',
    },
    PAID: { className: 'bg-success-soft text-success-soft-foreground', label: 'Paid' },
    REFUNDED: { className: 'bg-default text-muted', label: 'Refunded' },
};

function normalize(status: Status) {
    return status.toUpperCase().replace(/\s+/g, '_');
}

export function StatusBadge({ status }: StatusBadgeProps) {
    const key = normalize(status);
    const mapped = statusMap[key] ?? { className: 'bg-default text-muted', label: status };

    return (
        <span
            className={`inline-flex min-h-6 items-center whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium ${mapped.className}`}
        >
            {mapped.label}
        </span>
    );
}
