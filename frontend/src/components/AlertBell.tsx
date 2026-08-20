import { Tooltip } from '@heroui/react';
import { Bell, LoaderCircle } from 'lucide-react';
import { useNavigate } from 'react-router-dom';

import { useAlertOverview } from '@/hooks/useAlertOverview.tsx';

export function AlertBell() {
    const navigate = useNavigate();
    const { overview, loading, error, updatedAt } = useAlertOverview();
    const firing = overview?.firingTotal ?? 0;
    const label = firing > 0 ? `${firing} firing alerts` : 'No firing alerts';
    const status = error
        ? `Alert status unavailable: ${error}`
        : updatedAt
          ? `${label}. Updated ${updatedAt.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
          : loading
            ? 'Loading alerts'
            : label;

    return (
        <Tooltip>
            <Tooltip.Trigger>
                <button
                    aria-label={status}
                    className='relative inline-flex h-8 w-8 items-center justify-center rounded-lg text-muted transition-colors hover:bg-surface-secondary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary'
                    type='button'
                    onClick={() => navigate('/alerts')}
                >
                    {loading && !overview ? (
                        <LoaderCircle className='h-4 w-4 animate-spin' />
                    ) : (
                        <Bell className='h-4 w-4' />
                    )}
                    {firing > 0 && (
                        <span className='pointer-events-none absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-danger px-1 text-[10px] font-bold leading-none text-danger-foreground'>
                            {firing > 99 ? '99+' : firing}
                        </span>
                    )}
                    {error && firing === 0 && (
                        <span className='pointer-events-none absolute right-0 top-0 h-1.5 w-1.5 rounded-full bg-danger' />
                    )}
                </button>
            </Tooltip.Trigger>
            <Tooltip.Content>{status}</Tooltip.Content>
        </Tooltip>
    );
}
