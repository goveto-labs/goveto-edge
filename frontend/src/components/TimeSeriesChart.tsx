import type { MouseEvent } from 'react';

import { useEffect, useId, useRef, useState } from 'react';

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

const defaultWidth = 560;
const paddingX = 4;
const paddingTop = 20;
const paddingBottom = 32;
const dayMs = 86_400_000;

interface ChartPoint {
    x: number;
    y: number;
}

function round(value: number) {
    return Number(value.toFixed(2));
}

function smoothLine(points: ChartPoint[]) {
    if (points.length === 0) return '';
    if (points.length === 1) return `M ${points[0].x} ${points[0].y}`;
    let d = `M ${points[0].x} ${points[0].y}`;
    for (let index = 0; index < points.length - 1; index++) {
        const p0 = points[Math.max(0, index - 1)];
        const p1 = points[index];
        const p2 = points[index + 1];
        const p3 = points[Math.min(points.length - 1, index + 2)];
        const c1x = p1.x + (p2.x - p0.x) / 6;
        const c1y = p1.y + (p2.y - p0.y) / 6;
        const c2x = p2.x - (p3.x - p1.x) / 6;
        const c2y = p2.y - (p3.y - p1.y) / 6;
        d += ` C ${round(c1x)} ${round(c1y)}, ${round(c2x)} ${round(c2y)}, ${round(p2.x)} ${round(p2.y)}`;
    }
    return d;
}

export function TimeSeriesChart({
    data,
    series,
    ariaLabel,
    valueFormatter = (value) => value.toLocaleString(undefined, { maximumFractionDigits: 1 }),
    height = 224,
}: TimeSeriesChartProps) {
    const gradientPrefix = useId().replace(/[^a-zA-Z0-9]/g, '');
    const [hoverIndex, setHoverIndex] = useState<number | null>(null);
    const [width, setWidth] = useState(defaultWidth);
    const chartRef = useRef<HTMLDivElement>(null);

    useEffect(() => {
        if (!chartRef.current) return;
        const observer = new ResizeObserver(([entry]) => {
            setWidth(Math.max(240, Math.round(entry.contentRect.width)));
        });
        observer.observe(chartRef.current);
        return () => observer.disconnect();
    }, []);

    if (data.length === 0) {
        return (
            <div className='flex min-h-48 items-center justify-center text-sm text-muted'>
                No data
            </div>
        );
    }

    const innerWidth = width - paddingX * 2;
    const innerHeight = height - paddingTop - paddingBottom;
    const baseline = paddingTop + innerHeight;
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
    const stepMs =
        data.length > 1
            ? new Date(data[1].bucket).getTime() - new Date(data[0].bucket).getTime()
            : 0;
    const dateOnly = stepMs >= dayMs;
    const formatBucket = (bucket: string) =>
        new Date(bucket).toLocaleString(
            undefined,
            dateOnly
                ? { month: 'short', day: 'numeric' }
                : { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }
        );

    const paths = series.map((item) => {
        const points = data.map((point, index) => ({
            x: xAt(index),
            y: yAt(point.values[item.key] ?? 0),
        }));
        const line = smoothLine(points);
        const area = `${line} L ${round(points[points.length - 1].x)} ${baseline} L ${round(points[0].x)} ${baseline} Z`;
        return { ...item, line, area };
    });

    const handleMove = (event: MouseEvent<SVGSVGElement>) => {
        if (data.length === 1) {
            setHoverIndex(0);
            return;
        }
        const rect = event.currentTarget.getBoundingClientRect();
        const x = ((event.clientX - rect.left) / rect.width) * width;
        const index = Math.round(((x - paddingX) / innerWidth) * (data.length - 1));
        setHoverIndex(Math.max(0, Math.min(data.length - 1, index)));
    };

    const hovered = hoverIndex === null ? null : data[hoverIndex];
    const tooltipLeft =
        hoverIndex === null ? 0 : Math.min(88, Math.max(12, (xAt(hoverIndex) / width) * 100));

    return (
        <div className='flex h-full flex-col gap-3'>
            <div className='flex flex-wrap gap-4 text-sm text-muted'>
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
            <div ref={chartRef} className='relative min-h-0 flex-1'>
                <svg
                    aria-label={ariaLabel}
                    className='block w-full cursor-crosshair overflow-visible'
                    height={height}
                    onMouseLeave={() => setHoverIndex(null)}
                    onMouseMove={handleMove}
                    preserveAspectRatio='xMidYMid meet'
                    role='img'
                    viewBox={`0 0 ${width} ${height}`}
                >
                    <title>{ariaLabel}</title>
                    <defs>
                        {series.map((item) => (
                            <linearGradient
                                id={`${gradientPrefix}-${item.key}`}
                                key={item.key}
                                x1='0'
                                x2='0'
                                y1='0'
                                y2='1'
                            >
                                <stop offset='0%' stopColor={item.color} stopOpacity={0.22} />
                                <stop offset='100%' stopColor={item.color} stopOpacity={0.01} />
                            </linearGradient>
                        ))}
                    </defs>
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
                            </g>
                        );
                    })}
                    {paths.map((item) => (
                        <g key={item.key}>
                            <path d={item.area} fill={`url(#${gradientPrefix}-${item.key})`} />
                            <path
                                d={item.line}
                                fill='none'
                                stroke={item.color}
                                strokeLinecap='round'
                                strokeLinejoin='round'
                                strokeWidth='2.5'
                                vectorEffect='non-scaling-stroke'
                            />
                        </g>
                    ))}
                    {[0, 0.5, 1].map((ratio) => {
                        const y = paddingTop + innerHeight * ratio;
                        return (
                            <text
                                fill='currentColor'
                                fontSize='12'
                                key={ratio}
                                opacity={0.68}
                                paintOrder='stroke'
                                stroke='var(--surface)'
                                strokeWidth='4'
                                textAnchor='start'
                                x={paddingX + 6}
                                y={y + (ratio === 0 ? 13 : ratio === 1 ? -5 : 4)}
                            >
                                {valueFormatter(maxValue * (1 - ratio))}
                            </text>
                        );
                    })}
                    {hoverIndex !== null && (
                        <g>
                            <line
                                stroke='currentColor'
                                strokeOpacity={0.15}
                                x1={xAt(hoverIndex)}
                                x2={xAt(hoverIndex)}
                                y1={paddingTop}
                                y2={baseline}
                            />
                            {series.map((item) => (
                                <circle
                                    className='stroke-surface'
                                    cx={xAt(hoverIndex)}
                                    cy={yAt(data[hoverIndex].values[item.key] ?? 0)}
                                    fill={item.color}
                                    key={item.key}
                                    r='4'
                                    strokeWidth='2'
                                />
                            ))}
                        </g>
                    )}
                    {labelIndexes.map((index) => (
                        <text
                            fill='currentColor'
                            fontSize='12'
                            key={index}
                            opacity={0.68}
                            textAnchor={
                                index === 0 ? 'start' : index === data.length - 1 ? 'end' : 'middle'
                            }
                            x={xAt(index)}
                            y={height - 8}
                        >
                            {formatBucket(data[index].bucket)}
                        </text>
                    ))}
                </svg>
                {hovered && (
                    <div
                        className='pointer-events-none absolute top-0 z-50 -translate-x-1/2 rounded-xl border border-border bg-surface px-3 py-2 text-sm shadow-lg'
                        style={{ left: `${tooltipLeft}%` }}
                    >
                        <div className='mb-1 whitespace-nowrap text-xs font-medium text-muted'>
                            {formatBucket(hovered.bucket)}
                        </div>
                        <div className='space-y-1'>
                            {series.map((item) => (
                                <div
                                    className='flex items-center gap-2 whitespace-nowrap text-sm'
                                    key={item.key}
                                >
                                    <span
                                        className='h-2 w-2 rounded-full'
                                        style={{ backgroundColor: item.color }}
                                    />
                                    <span className='text-muted'>{item.label}</span>
                                    <span className='ml-auto pl-3 font-semibold'>
                                        {valueFormatter(hovered.values[item.key] ?? 0)}
                                    </span>
                                </div>
                            ))}
                        </div>
                    </div>
                )}
            </div>
        </div>
    );
}
