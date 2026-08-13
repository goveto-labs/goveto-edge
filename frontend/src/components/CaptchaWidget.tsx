import { Spinner } from '@heroui/react';
import { useEffect, useRef, useState } from 'react';

import {
    type CaptchaProvider,
    isCaptchaProvider,
    loadCaptchaScript,
} from '@/components/captchaWidget.ts';

interface CaptchaAPI {
    render: (
        container: HTMLElement,
        options: {
            sitekey: string;
            callback: (token: string) => void;
            'error-callback': () => void;
            'expired-callback': () => void;
            theme: 'auto' | 'light';
        }
    ) => string | number;
    reset: (widgetId: string | number) => void;
    remove?: (widgetId: string | number) => void;
}

declare global {
    interface Window {
        turnstile?: CaptchaAPI;
        grecaptcha?: CaptchaAPI;
    }
}

interface CaptchaWidgetProps {
    provider: string;
    siteKey: string;
    resetKey: number;
    onToken: (token: string) => void;
}

function providerAPI(provider: CaptchaProvider) {
    return provider === 'cloudflare' ? window.turnstile : window.grecaptcha;
}

export function CaptchaWidget({ provider, siteKey, resetKey, onToken }: CaptchaWidgetProps) {
    const containerRef = useRef<HTMLDivElement>(null);
    const widgetRef = useRef<string | number | null>(null);
    const onTokenRef = useRef(onToken);
    const previousResetKey = useRef(resetKey);
    const [error, setError] = useState('');
    const [loading, setLoading] = useState(true);

    onTokenRef.current = onToken;

    useEffect(() => {
        if (previousResetKey.current === resetKey) return;
        previousResetKey.current = resetKey;
        if (widgetRef.current === null || !isCaptchaProvider(provider)) return;
        providerAPI(provider)?.reset(widgetRef.current);
        onTokenRef.current('');
    }, [provider, resetKey]);

    useEffect(() => {
        const container = containerRef.current;
        if (!container || !isCaptchaProvider(provider) || siteKey.trim() === '') {
            setError('CAPTCHA is not configured.');
            setLoading(false);
            return;
        }

        let active = true;
        setError('');
        setLoading(true);
        onTokenRef.current('');
        loadCaptchaScript(provider)
            .then(() => {
                if (!active) return;
                const api = providerAPI(provider);
                if (!api) throw new Error('The CAPTCHA provider did not initialize.');
                container.replaceChildren();
                widgetRef.current = api.render(container, {
                    sitekey: siteKey,
                    callback: (token) => {
                        if (active) onTokenRef.current(token);
                    },
                    'expired-callback': () => {
                        if (active) onTokenRef.current('');
                    },
                    'error-callback': () => {
                        if (active) {
                            onTokenRef.current('');
                            setError('CAPTCHA verification could not start. Try again.');
                        }
                    },
                    theme: provider === 'cloudflare' ? 'auto' : 'light',
                });
                setLoading(false);
            })
            .catch((loadError: unknown) => {
                if (!active) return;
                setLoading(false);
                setError(
                    loadError instanceof Error
                        ? loadError.message
                        : 'Unable to load the CAPTCHA provider.'
                );
            });

        return () => {
            active = false;
            const widgetId = widgetRef.current;
            if (widgetId !== null) providerAPI(provider)?.remove?.(widgetId);
            widgetRef.current = null;
            container.replaceChildren();
        };
    }, [provider, siteKey]);

    return (
        <fieldset aria-label='CAPTCHA challenge' className='min-h-16 border-0 p-0'>
            {loading && (
                <div className='flex h-16 items-center justify-center'>
                    <Spinner size='sm' />
                </div>
            )}
            <div className={loading ? 'hidden' : undefined} ref={containerRef} />
            {error && (
                <p className='mt-2 text-sm text-danger' role='alert'>
                    {error}
                </p>
            )}
        </fieldset>
    );
}
