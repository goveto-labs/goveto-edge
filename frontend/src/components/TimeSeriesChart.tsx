export interface TimeSeriesDatum {
    bucket: string;
    values: Record<string, number>;
}

interface ChartSeries {
    key: string;
    label: string;
    color: string;
}

interface TimeSeriesChartProps {
    data: TimeSeriesDatum[];
    series: ChartSeries[];
    ariaLabel: string;
    valueFormatter?: (value: number) => string;
    height?: number;
}

export function TimeSeriesChart({
    data,
    series,
    ariaLabel,
    valueFormatter = (value) => value.toLocaleString(undefined, { maximumFractionDigits: 1 }),
    height = 240,
}: TimeSeriesChartProps) {
    if (data.length === 0) {
        return (
            <div className='flex min-h-48 items-center justify-center text-sm text-muted'>
                No data
            </div>
        );
    }

    const width = 900;
    const paddingX = 44;
    const paddingTop = 20;
    const paddingBottom = 32;
    const innerWidth = width - paddingX * 2;
    const innerHeight = height - paddingTop - paddingBottom;
    const maxValue = Math.max(
        1,
        ...data.flatMap((point) => series.map((item) => point.values[item.key] ?? 0))
    );
    const xAt = (index: number) =>
        paddingX + (data.length === 1 ? innerWidth / 2 : (index / (data.length - 1)) * innerWidth);
    const yAt = (value: number) => paddingTop + innerHeight - (value / maxValue) * innerHeight;
    const labelIndexes = Array.from(
        new Set([0, Math.floor((data.length - 1) / 2), data.length - 1])
    );

    return (
        <div className='space-y-3'>
            <div className='flex flex-wrap gap-4 text-xs text-muted'>
                {series.map((item) => (
                    <span className='inline-flex items-center gap-1.5' key={item.key}>
                        <span
                            className='h-2 w-2 rounded-full'
                            style={{ backgroundColor: item.color }}
                        />
                        {item.label}
                    </span>
                ))}
            </div>
            <svg
                aria-label={ariaLabel}
                className='w-full overflow-visible'
                height={height}
                preserveAspectRatio='xMidYMid meet'
                role='img'
                viewBox={`0 0 ${width} ${height}`}
            >
                <title>{ariaLabel}</title>
                {[0, 0.5, 1].map((ratio) => {
                    const y = paddingTop + innerHeight * ratio;
                    return (
                        <g key={ratio}>
                            <line
                                stroke='currentColor'
                                strokeDasharray='4 5'
                                strokeOpacity={0.12}
                                x1={paddingX}
                                x2={width - paddingX}
                                y1={y}
                                y2={y}
                            />
                            <text
                                fill='currentColor'
                                fontSize='10'
                                opacity={0.55}
                                textAnchor='end'
                                x={paddingX - 8}
                                y={y + 3}
                            >
                                {valueFormatter(maxValue * (1 - ratio))}
                            </text>
                        </g>
                    );
                })}
                {series.map((item) => {
                    const path = data
                        .map((point, index) => {
                            const value = point.values[item.key] ?? 0;
                            return `${index === 0 ? 'M' : 'L'} ${xAt(index)} ${yAt(value)}`;
                        })
                        .join(' ');
                    return (
                        <g key={item.key}>
                            <path
                                d={path}
                                fill='none'
                                stroke={item.color}
                                strokeLinecap='round'
                                strokeLinejoin='round'
                                strokeWidth='2.5'
                                vectorEffect='non-scaling-stroke'
                            />
                            {data.map((point, index) => {
                                const value = point.values[item.key] ?? 0;
                                return (
                                    <circle
                                        cx={xAt(index)}
                                        cy={yAt(value)}
                                        fill={item.color}
                                        key={`${item.key}-${point.bucket}`}
                                        r='2.5'
                                        vectorEffect='non-scaling-stroke'
                                    >
                                        <title>{`${item.label}: ${valueFormatter(value)} · ${new Date(point.bucket).toLocaleString()}`}</title>
                                    </circle>
                                );
                            })}
                        </g>
                    );
                })}
                {labelIndexes.map((index) => (
                    <text
                        fill='currentColor'
                        fontSize='10'
                        key={index}
                        opacity={0.55}
                        textAnchor={
                            index === 0 ? 'start' : index === data.length - 1 ? 'end' : 'middle'
                        }
                        x={xAt(index)}
                        y={height - 8}
                    >
                        {new Date(data[index].bucket).toLocaleString(undefined, {
                            month: 'short',
                            day: 'numeric',
                            hour: '2-digit',
                            minute: '2-digit',
                        })}
                    </text>
                ))}
            </svg>
        </div>
    );
}
