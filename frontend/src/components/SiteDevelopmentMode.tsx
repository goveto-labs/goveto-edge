import { FlaskConical } from 'lucide-react';

import { ContentCard } from '@/components/ContentCard.tsx';
import { ToggleSwitch } from '@/components/ToggleSwitch.tsx';

export function SiteDevelopmentMode({
    enabled,
    disabled,
    onToggle,
}: {
    enabled: boolean;
    disabled: boolean;
    onToggle: () => void;
}) {
    return (
        <ContentCard noPadding>
            <div className='flex flex-col gap-4 px-5 py-5 sm:flex-row sm:items-center sm:justify-between lg:px-6'>
                <div className='flex items-center gap-3'>
                    <span
                        className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-lg ${
                            enabled
                                ? 'bg-warning/15 text-warning'
                                : 'bg-surface-secondary text-muted'
                        }`}
                    >
                        <FlaskConical className='h-5 w-5' />
                    </span>
                    <div>
                        <h2 className='text-base font-semibold'>Development mode</h2>
                        <p className='mt-0.5 text-xs text-muted'>
                            Temporarily bypass all cache rules and fetch every response directly
                            from the origin. Turn it off to restore normal cache behavior.
                        </p>
                    </div>
                </div>
                <div className='flex shrink-0 items-center gap-2'>
                    <span
                        className={`text-xs font-medium ${enabled ? 'text-warning' : 'text-muted'}`}
                    >
                        {enabled ? 'On' : 'Off'}
                    </span>
                    <ToggleSwitch
                        isDisabled={disabled}
                        isSelected={enabled}
                        label='Development mode'
                        onChange={onToggle}
                    />
                </div>
            </div>
            {enabled && (
                <div className='border-t border-warning/20 bg-warning/10 px-5 py-3 text-xs leading-5 text-warning lg:px-6'>
                    Development mode is on — every request bypasses the cache and goes straight to
                    the origin. Nothing is read from or written to the cache while this is enabled.
                </div>
            )}
        </ContentCard>
    );
}
