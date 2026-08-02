import { Input } from '@heroui/react';
import { useState } from 'react';

import { SelectField } from '@/components/SelectField.tsx';

export type DurationUnit = 'SECONDS' | 'MINUTES' | 'HOURS' | 'DAYS';

const unitFactors: Record<DurationUnit, number> = {
    SECONDS: 1,
    MINUTES: 60,
    HOURS: 60 * 60,
    DAYS: 24 * 60 * 60,
};

const unitOptions = [
    { id: 'SECONDS', label: 'Seconds' },
    { id: 'MINUTES', label: 'Minutes' },
    { id: 'HOURS', label: 'Hours' },
    { id: 'DAYS', label: 'Days' },
];

function preferredUnit(seconds: number): DurationUnit {
    if (seconds >= unitFactors.DAYS) return 'DAYS';
    if (seconds >= unitFactors.HOURS) return 'HOURS';
    if (seconds >= unitFactors.MINUTES) return 'MINUTES';
    return 'SECONDS';
}

function displayValue(seconds: number, factor: number) {
    const value = seconds / factor;
    return Number.isInteger(value) ? String(value) : String(Number(value.toPrecision(8)));
}

export function formatDuration(seconds: number) {
    const unit = preferredUnit(seconds);
    const value = displayValue(seconds, unitFactors[unit]);
    const suffix: Record<DurationUnit, string> = {
        SECONDS: 'sec',
        MINUTES: 'min',
        HOURS: 'hr',
        DAYS: 'day',
    };
    return `${value} ${suffix[unit]}`;
}

export function DurationInput({
    id,
    seconds,
    minimumSeconds,
    maximumSeconds,
    onChange,
}: {
    id: string;
    seconds: number;
    minimumSeconds: number;
    maximumSeconds: number;
    onChange: (seconds: number) => void;
}) {
    const [unit, setUnit] = useState<DurationUnit>(() => preferredUnit(seconds));
    const factor = unitFactors[unit];

    return (
        <div className='grid min-w-0 grid-cols-[minmax(0,1fr)_7.5rem] gap-2'>
            <Input
                id={id}
                min={minimumSeconds / factor}
                max={maximumSeconds / factor}
                step={unit === 'SECONDS' ? 1 : 'any'}
                type='number'
                value={displayValue(seconds, factor)}
                variant='secondary'
                onChange={(event) =>
                    onChange(Math.round(Number(event.target.value) * unitFactors[unit]))
                }
            />
            <SelectField
                ariaLabel={`${id} unit`}
                options={unitOptions}
                value={unit}
                variant='secondary'
                onChange={(value) => setUnit(value as DurationUnit)}
            />
        </div>
    );
}
