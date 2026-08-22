interface ParsedVersion {
    core: [number, number, number];
    prerelease: string[];
}

function parseVersion(value: string | undefined): ParsedVersion | null {
    const match = value
        ?.trim()
        .match(
            /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/
        );
    if (!match) return null;
    const prerelease = match[4]?.split('.') ?? [];
    if (
        prerelease.some(
            (identifier) =>
                identifier === '' || (/^\d+$/.test(identifier) && /^0\d+/.test(identifier))
        )
    ) {
        return null;
    }
    return {
        core: [Number(match[1]), Number(match[2]), Number(match[3])],
        prerelease,
    };
}

function comparePrerelease(left: string[], right: string[]): number {
    if (left.length === 0 || right.length === 0) {
        return left.length === right.length ? 0 : left.length === 0 ? 1 : -1;
    }
    for (let index = 0; index < Math.max(left.length, right.length); index++) {
        const leftIdentifier = left[index];
        const rightIdentifier = right[index];
        if (leftIdentifier === undefined || rightIdentifier === undefined) {
            return leftIdentifier === rightIdentifier ? 0 : leftIdentifier === undefined ? -1 : 1;
        }
        if (leftIdentifier === rightIdentifier) continue;
        const leftNumeric = /^\d+$/.test(leftIdentifier);
        const rightNumeric = /^\d+$/.test(rightIdentifier);
        if (leftNumeric && rightNumeric)
            return Number(leftIdentifier) < Number(rightIdentifier) ? -1 : 1;
        if (leftNumeric !== rightNumeric) return leftNumeric ? -1 : 1;
        return leftIdentifier < rightIdentifier ? -1 : 1;
    }
    return 0;
}

export function needsVersionUpgrade(current: string | undefined, target: string): boolean {
    const parsedTarget = parseVersion(target);
    if (!parsedTarget) return false;
    const parsedCurrent = parseVersion(current);
    if (!parsedCurrent) return true;
    for (let index = 0; index < parsedCurrent.core.length; index++) {
        if (parsedCurrent.core[index] !== parsedTarget.core[index]) {
            return parsedCurrent.core[index] < parsedTarget.core[index];
        }
    }
    return comparePrerelease(parsedCurrent.prerelease, parsedTarget.prerelease) < 0;
}
