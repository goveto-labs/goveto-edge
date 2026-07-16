import type { DistributionItem } from '@/api';

interface RankingBarsProps {
    items: DistributionItem[];
    limit?: number;
    emptyText?: string;
}

function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const unit = Math.min(
        Math.max(0, Math.floor(Math.log(bytes) / Math.log(1024))),
        units.length - 1
    );
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

export function RankingBars({
    items,
    limit = 10,
    emptyText = 'No requests recorded in this period.',
}: RankingBarsProps) {
    const shown = items.slice(0, limit);
    if (shown.length === 0) {
        return (
            <div className='flex min-h-32 items-center justify-center text-sm text-muted'>
                {emptyText}
            </div>
        );
    }
    const max = Math.max(1, ...shown.map((item) => item.requests));

    return (
        <ol className='space-y-3'>
            {shown.map((item, index) => (
                <li className='space-y-1.5' key={item.value || index}>
                    <div className='flex items-baseline gap-3'>
                        <span className='w-5 shrink-0 text-right text-sm font-medium text-muted'>
                            {index + 1}
                        </span>
                        <span
                            className='min-w-0 flex-1 truncate font-mono text-sm'
                            title={item.value}
                        >
                            {item.value || '(empty)'}
                        </span>
                        <span className='shrink-0 text-sm font-semibold'>
                            {item.requests.toLocaleString()}
                        </span>
                        <span className='w-16 shrink-0 text-right text-xs text-muted'>
                            {formatBytes(item.ingress_bytes + item.egress_bytes)}
                        </span>
                    </div>
                    <div className='ml-8 h-1.5 overflow-hidden rounded-full bg-surface-secondary'>
                        <div
                            className='h-full rounded-full bg-linear-to-r from-primary/50 to-primary'
                            style={{ width: `${Math.max(2, (item.requests / max) * 100)}%` }}
                        />
                    </div>
                </li>
            ))}
        </ol>
    );
}
