import { useEffect, useState } from 'react';

import { getConsoleVersion } from '@/api/version.ts';

/**
 * Console version label: backend release version, or `dev-<startup stamp>`
 * when the backend runs a dev build. Returns null until resolved.
 */
export function useConsoleVersion(): string | null {
    const [version, setVersion] = useState<string | null>(null);

    useEffect(() => {
        let alive = true;
        getConsoleVersion().then((value) => {
            if (alive) setVersion(value);
        });
        return () => {
            alive = false;
        };
    }, []);

    return version;
}
