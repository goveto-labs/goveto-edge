import { Label, ListBox, Select } from '@heroui/react';

const emptyValueKey = '__goveto_empty_select_value__';

export interface SelectOption {
    id: string;
    label: string;
    textValue?: string;
}

interface SelectFieldProps {
    options: SelectOption[];
    value: string;
    onChange: (value: string) => void;
    label?: string;
    ariaLabel?: string;
    className?: string;
    id?: string;
    placeholder?: string;
    isDisabled?: boolean;
    isRequired?: boolean;
    variant?: 'primary' | 'secondary';
}

export function SelectField({
    options,
    value,
    onChange,
    label,
    ariaLabel,
    className,
    id,
    placeholder,
    isDisabled,
    isRequired,
    variant,
}: SelectFieldProps) {
    const hasEmptyOption = options.some((option) => option.id === '');
    const selectedValue = value === '' ? (hasEmptyOption ? emptyValueKey : null) : value;

    return (
        <Select
            aria-label={ariaLabel ?? label}
            className={className}
            id={id}
            isDisabled={isDisabled}
            isRequired={isRequired}
            placeholder={placeholder}
            value={selectedValue}
            variant={variant}
            onChange={(key) => {
                if (key === null) return;
                const nextValue = String(key);
                onChange(nextValue === emptyValueKey ? '' : nextValue);
            }}
        >
            <Label className={label ? undefined : 'sr-only'}>{label ?? ariaLabel}</Label>
            <Select.Trigger>
                <Select.Value />
                <Select.Indicator />
            </Select.Trigger>
            <Select.Popover>
                <ListBox>
                    {options.map((option) => {
                        const key = option.id === '' ? emptyValueKey : option.id;
                        return (
                            <ListBox.Item
                                id={key}
                                key={key}
                                textValue={option.textValue ?? option.label}
                            >
                                {option.label}
                                <ListBox.ItemIndicator />
                            </ListBox.Item>
                        );
                    })}
                </ListBox>
            </Select.Popover>
        </Select>
    );
}
