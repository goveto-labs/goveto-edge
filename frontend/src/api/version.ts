interface HealthStatus {
    status?: string;
    version?: string;
    startedAt?: string;
}

/** Accepts both the enveloped (`{code, data}`) and raw health response shapes. */
interface HealthBody extends HealthStatus {
    code?: string;
    data?: HealthStatus;
}

function formatStartupStamp(iso: string | undefined): string | null {
    if (!iso) return null;
    const time = new Date(iso);
    if (Number.isNaN(time.getTime())) return null;
    const pad = (value: number) => String(value).padStart(2, '0');
    const date = `${time.getFullYear()}${pad(time.getMonth() + 1)}${pad(time.getDate())}`;
    const clock = `${pad(time.getHours())}${pad(time.getMinutes())}`;
    return `${date}-${clock}`;
}

async function requestConsoleVersion(): Promise<string> {
    try {
        const response = await fetch('/health/live', { headers: { Accept: 'application/json' } });
        if (!response.ok) return 'dev';
        const body = (await response.json()) as HealthBody;
        const status: HealthStatus = body.data ?? body;
        const version = status.version?.trim();
        if (version && version !== 'dev') return version;
        const stamp = formatStartupStamp(status.startedAt);
        return stamp ? `dev-${stamp}` : 'dev';
    } catch {
        return 'dev';
    }
}

let versionPromise: Promise<string> | null = null;

/**
 * Resolves the console version label. Release builds report the backend
 * version; dev builds fall back to `dev-<backend startup stamp>`.
 * The result is cached for the session so multiple Sidebar instances
 * (desktop rail + mobile drawer) share one request.
 */
export function getConsoleVersion(): Promise<string> {
    versionPromise ??= requestConsoleVersion();
    return versionPromise;
}
