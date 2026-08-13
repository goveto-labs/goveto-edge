export class RequestGeneration {
    private value = 0;

    next() {
        this.value += 1;
        return this.value;
    }

    invalidate() {
        this.value += 1;
    }

    isCurrent(value: number, signal: AbortSignal) {
        return value === this.value && !signal.aborted;
    }
}
