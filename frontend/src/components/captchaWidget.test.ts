import { expect, test } from 'vitest';

import { captchaScriptURLs, isCaptchaProvider } from './captchaWidget.ts';

test('CAPTCHA providers map only to fixed official script URLs', () => {
    expect(Object.keys(captchaScriptURLs).sort()).toEqual(['cloudflare', 'recaptcha']);
    expect(captchaScriptURLs.cloudflare).toBe(
        'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
    );
    expect(captchaScriptURLs.recaptcha).toBe(
        'https://www.google.com/recaptcha/api.js?render=explicit'
    );
    expect(isCaptchaProvider('https://attacker.example/widget.js')).toBe(false);
    expect(isCaptchaProvider('turnstile')).toBe(false);
});
