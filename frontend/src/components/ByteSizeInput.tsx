import { Input } from '@heroui/react';
import { useState } from 'react';

import { SelectField } from '@/components/SelectField.tsx';

export type ByteSizeUnit = 'B' | 'KB' | 'MB' | 'GB';

const unitFactors: Record<ByteSizeUnit, number> = {
    B: 1,
    KB: 1024,
    MB: 1024 ** 2,
    GB: 1024 ** 3,
};

const unitOptions = (Object.keys(unitFactors) as ByteSizeUnit[]).map((unit) => ({
    id: unit,
    label: unit,
}));

function displayValue(bytes: number, factor: number) {
    const value = bytes / factor;
    return Number.isInteger(value) ? String(value) : String(Number(value.toPrecision(8)));
}

export function ByteSizeInput({
    id,
    bytes,
    defaultUnit,
    minimumBytes,
    maximumBytes,
    onChange,
}: {
    id: string;
    bytes: number;
    defaultUnit: ByteSizeUnit;
    minimumBytes: number;
    maximumBytes: number;
    onChange: (bytes: number) => void;
}) {
    const [unit, setUnit] = useState<ByteSizeUnit>(defaultUnit);
    const factor = unitFactors[unit];

    return (
        <div className='grid min-w-0 grid-cols-[minmax(0,1fr)_6rem] gap-2'>
            <Input
                id={id}
                min={minimumBytes / factor}
                max={maximumBytes / factor}
                step={unit === 'B' ? 1 : 'any'}
                type='number'
                value={displayValue(bytes, factor)}
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
                onChange={(value) => setUnit(value as ByteSizeUnit)}
            />
        </div>
    );
}
