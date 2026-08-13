export type CaptchaProvider = 'cloudflare' | 'recaptcha';

export const captchaScriptURLs: Record<CaptchaProvider, string> = {
    cloudflare: 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit',
    recaptcha: 'https://www.google.com/recaptcha/api.js?render=explicit',
};

export function isCaptchaProvider(value: string): value is CaptchaProvider {
    return value === 'cloudflare' || value === 'recaptcha';
}

const scriptLoads = new Map<CaptchaProvider, Promise<void>>();

export function loadCaptchaScript(provider: CaptchaProvider): Promise<void> {
    const existing = scriptLoads.get(provider);
    if (existing) return existing;

    const loaded = new Promise<void>((resolve, reject) => {
        const id = `captcha-script-${provider}`;
        const current = document.getElementById(id) as HTMLScriptElement | null;
        const apiReady =
            provider === 'cloudflare'
                ? window.turnstile !== undefined
                : window.grecaptcha !== undefined;
        if (current && apiReady) {
            resolve();
            return;
        }
        const script = current ?? document.createElement('script');
        const onLoad = () => resolve();
        const onError = () => {
            scriptLoads.delete(provider);
            script.remove();
            reject(new Error('Unable to load the CAPTCHA provider.'));
        };
        script.addEventListener('load', onLoad, { once: true });
        script.addEventListener('error', onError, { once: true });
        if (!current) {
            script.id = id;
            script.src = captchaScriptURLs[provider];
            script.async = true;
            script.defer = true;
            document.head.appendChild(script);
        }
    });
    scriptLoads.set(provider, loaded);
    return loaded;
}
