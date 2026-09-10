/**
 * Theme-driven chart palette. Values are CSS variable references resolved by
 * the current theme (see src/styles/theme.css), so series follow light/dark
 * mode automatically. Pass these to TimeSeriesChart/DonutChart/RankingBars
 * instead of hardcoded hex colors.
 */
export const chartColors = {
    /** Primary series — theme accent (teal). */
    primary: 'var(--chart-1)',
    /** Secondary series — blue. */
    secondary: 'var(--chart-2)',
    /** Tertiary series — violet. */
    tertiary: 'var(--chart-3)',
    /** Attention / p95 / CPU — amber. */
    warning: 'var(--chart-4)',
    /** Errors / WAF hits — red. */
    danger: 'var(--chart-5)',
    /** Neutral / idle series. */
    neutral: 'var(--chart-6)',
} as const;

export type ChartColorKey = keyof typeof chartColors;
