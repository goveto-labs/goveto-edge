type Status = 'online' | 'offline' | 'pending' | 'failed' | 'completed' | 'disabled' | string;

interface StatusBadgeProps {
    status: Status;
}

const statusMap: Record<string, { className: string; label: string }> = {
    ONLINE: { className: 'bg-success/15 text-success', label: 'Online' },
    OFFLINE: { className: 'bg-danger/15 text-danger', label: 'Offline' },
    DISABLED: { className: 'bg-default text-muted', label: 'Disabled' },
    INSTALLING: { className: 'bg-warning/15 text-warning', label: 'Installing' },
    INSTALL_FAILED: { className: 'bg-danger/15 text-danger', label: 'Install failed' },
    PENDING: { className: 'bg-warning/15 text-warning', label: 'Pending' },
    RUNNING: { className: 'bg-warning/15 text-warning', label: 'Running' },
    COMPLETED: { className: 'bg-success/15 text-success', label: 'Completed' },
    FAILED: { className: 'bg-danger/15 text-danger', label: 'Failed' },
    ACTIVE: { className: 'bg-success/15 text-success', label: 'Active' },
    DEPLOYING: { className: 'bg-warning/15 text-warning', label: 'Not active yet' },
    EXPIRING: { className: 'bg-warning/15 text-warning', label: 'Expiring soon' },
    EXPIRED: { className: 'bg-danger/15 text-danger', label: 'Expired' },
    RENEWAL_FAILED: { className: 'bg-danger/15 text-danger', label: 'Renewal failed' },
    DEPLOYMENT_FAILED: { className: 'bg-danger/15 text-danger', label: 'Publish failed' },
    PAID: { className: 'bg-success/15 text-success', label: 'Paid' },
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
