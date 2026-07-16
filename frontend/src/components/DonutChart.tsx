export interface DonutSlice {
    label: string;
    value: number;
    color: string;
}

interface DonutChartProps {
    slices: DonutSlice[];
    centerLabel?: string;
    emptyText?: string;
    ariaLabel?: string;
    compact?: boolean;
}

const size = 176;
const center = size / 2;
const radius = 68;
const strokeWidth = 22;

export function DonutChart({
    slices,
    centerLabel = 'Requests',
    emptyText = 'No requests in this period',
    ariaLabel = 'Distribution chart',
    compact = false,
}: DonutChartProps) {
    const total = slices.reduce((sum, slice) => sum + slice.value, 0);
    let offset = 0;
    const segments = slices
        .filter((slice) => slice.value > 0)
        .map((slice) => {
            const pct = total > 0 ? (slice.value / total) * 100 : 0;
            const segment = { ...slice, pct, start: offset };
            offset += pct;
            return segment;
        });
    const gap = segments.length > 1 ? 0.8 : 0;

    return (
        <div
            className={`flex items-center gap-4 ${compact ? 'flex-col' : 'flex-col sm:flex-row sm:gap-6'}`}
        >
            <div className='relative shrink-0'>
                <svg
                    aria-label={ariaLabel}
                    className='-rotate-90'
                    height={size}
                    role='img'
                    viewBox={`0 0 ${size} ${size}`}
                    width={size}
                >
                    <title>{ariaLabel}</title>
                    <circle
                        cx={center}
                        cy={center}
                        fill='none'
                        r={radius}
                        stroke='currentColor'
                        strokeOpacity={0.08}
                        strokeWidth={strokeWidth}
                    />
                    {segments.map((segment) => (
                        <circle
                            cx={center}
                            cy={center}
                            fill='none'
                            key={segment.label}
                            pathLength={100}
                            r={radius}
                            stroke={segment.color}
                            strokeDasharray={`${Math.max(segment.pct - gap, 0)} ${100 - segment.pct + gap}`}
                            strokeDashoffset={-segment.start}
                            strokeWidth={strokeWidth}
                        >
                            <title>{`${segment.label}: ${segment.value.toLocaleString()} (${segment.pct.toFixed(1)}%)`}</title>
                        </circle>
                    ))}
                </svg>
                <div className='pointer-events-none absolute inset-0 flex flex-col items-center justify-center'>
                    <div className='text-2xl font-bold tracking-tight'>
                        {total.toLocaleString()}
                    </div>
                    <div className='text-sm text-muted'>{total > 0 ? centerLabel : 'No data'}</div>
                </div>
            </div>
            <div className='w-full min-w-0 flex-1'>
                {segments.length === 0 ? (
                    <p className='text-center text-sm text-muted sm:text-left'>{emptyText}</p>
                ) : (
                    <ul className='space-y-2.5'>
                        {segments.map((segment) => (
                            <li className='flex items-center gap-2.5' key={segment.label}>
                                <span
                                    className='h-2.5 w-2.5 shrink-0 rounded-full'
                                    style={{ backgroundColor: segment.color }}
                                />
                                <span
                                    className='min-w-0 flex-1 truncate font-mono text-sm'
                                    title={segment.label}
                                >
                                    {segment.label}
                                </span>
                                <span className='shrink-0 text-sm text-muted'>
                                    {segment.value.toLocaleString()} · {segment.pct.toFixed(1)}%
                                </span>
                            </li>
                        ))}
                    </ul>
                )}
            </div>
        </div>
    );
}
