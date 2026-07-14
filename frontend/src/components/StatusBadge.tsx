import { Badge } from '@heroui/react';

type Status = 'online' | 'offline' | 'pending' | 'failed' | 'completed' | 'disabled' | string;

interface StatusBadgeProps {
    status: Status;
}

const statusMap: Record<
    string,
    { color: 'success' | 'danger' | 'warning' | 'default'; label: string }
> = {
    ONLINE: { color: 'success', label: 'Online' },
    OFFLINE: { color: 'danger', label: 'Offline' },
    DISABLED: { color: 'default', label: 'Disabled' },
    INSTALL_FAILED: { color: 'danger', label: 'Install failed' },
    PENDING: { color: 'warning', label: 'Pending' },
    RUNNING: { color: 'warning', label: 'Running' },
    COMPLETED: { color: 'success', label: 'Completed' },
    FAILED: { color: 'danger', label: 'Failed' },
    PAID: { color: 'success', label: 'Paid' },
    REFUNDED: { color: 'default', label: 'Refunded' },
};

function normalize(status: Status) {
    return status.toUpperCase().replace(/\s+/g, '_');
}

export function StatusBadge({ status }: StatusBadgeProps) {
    const key = normalize(status);
    const mapped = statusMap[key] ?? { color: 'default', label: status };

    return (
        <Badge color={mapped.color} size='sm' variant='soft'>
            {mapped.label}
        </Badge>
    );
}
