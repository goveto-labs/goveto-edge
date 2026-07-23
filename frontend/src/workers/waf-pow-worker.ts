import { scrypt } from '@noble/hashes/scrypt.js';

type WorkerRequest = {
    nonce: string;
    salt: string;
    target: string;
    maxCounter: number;
    start: number;
    step: number;
    N: number;
    r: number;
    p: number;
};

function decodeBase64URL(value: string) {
    value = value.replace(/-/g, '+').replace(/_/g, '/');
    value += '='.repeat((4 - (value.length % 4)) % 4);
    return Uint8Array.from(atob(value), (char) => char.charCodeAt(0));
}

function encodeBase64URL(value: Uint8Array) {
    let binary = '';
    for (const byte of value) binary += String.fromCharCode(byte);
    return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

self.onmessage = ({ data }: MessageEvent<WorkerRequest>) => {
    const nonce = decodeBase64URL(data.nonce);
    const salt = decodeBase64URL(data.salt);
    const target = decodeBase64URL(data.target);
    const domain = new TextEncoder().encode('goveto-edge/waf-scrypt/v3\0');
    const password = new Uint8Array(domain.length + nonce.length + 4);
    password.set(domain);
    password.set(nonce, domain.length);
    const view = new DataView(password.buffer);

    for (let counter = data.start; counter <= data.maxCounter; counter += data.step) {
        view.setUint32(password.length - 4, counter, false);
        const key = scrypt(password, salt, {
            N: data.N,
            r: data.r,
            p: data.p,
            dkLen: 32,
            maxmem: 64 * 1024 * 1024,
        });
        let matches = true;
        for (let index = 0; index < target.length; index++) {
            if (key[index] !== target[index]) {
                matches = false;
                break;
            }
        }
        if (matches) {
            self.postMessage({ type: 'solved', counter, key: encodeBase64URL(key) });
            return;
        }
        self.postMessage({ type: 'progress', counter });
    }
    self.postMessage({ type: 'exhausted' });
};
