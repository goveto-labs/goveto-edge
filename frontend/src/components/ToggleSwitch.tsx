import { Switch } from '@heroui/react';

interface ToggleSwitchProps {
    label: string;
    isSelected: boolean;
    isDisabled?: boolean;
    onChange: (selected: boolean) => void;
}

export function ToggleSwitch({
    label,
    isSelected,
    isDisabled = false,
    onChange,
}: ToggleSwitchProps) {
    return (
        <Switch
            aria-label={label}
            isDisabled={isDisabled}
            isSelected={isSelected}
            onChange={onChange}
        >
            <Switch.Content>
                <Switch.Control>
                    <Switch.Thumb />
                </Switch.Control>
            </Switch.Content>
        </Switch>
    );
}
