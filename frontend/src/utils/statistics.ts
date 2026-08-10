export function percentile(values: number[], quantile: number) {
    const finite = values.filter(Number.isFinite).sort((left, right) => left - right);
    if (finite.length === 0) return 0;
    const bounded = Math.min(1, Math.max(0, quantile));
    return finite[Math.max(0, Math.ceil(bounded * finite.length) - 1)];
}
