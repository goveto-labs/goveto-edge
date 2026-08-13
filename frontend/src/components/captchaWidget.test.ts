import assert from 'node:assert/strict';
import test from 'node:test';

import { captchaScriptURLs, isCaptchaProvider } from './captchaWidget.ts';

test('CAPTCHA providers map only to fixed official script URLs', () => {
    assert.deepEqual(Object.keys(captchaScriptURLs).sort(), ['cloudflare', 'recaptcha']);
    assert.equal(
        captchaScriptURLs.cloudflare,
        'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
    );
    assert.equal(
        captchaScriptURLs.recaptcha,
        'https://www.google.com/recaptcha/api.js?render=explicit'
    );
    assert.equal(isCaptchaProvider('https://attacker.example/widget.js'), false);
    assert.equal(isCaptchaProvider('turnstile'), false);
});
