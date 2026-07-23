import { build } from 'esbuild';

await build({
    entryPoints: ['src/workers/waf-pow-worker.ts'],
    bundle: true,
    format: 'iife',
    platform: 'browser',
    target: 'es2020',
    minify: true,
    legalComments: 'inline',
    banner: {
        js: '/* Scrypt implementation bundled from @noble/hashes (MIT, Copyright Paul Miller). */',
    },
    outfile: '../caddy/waf/templates/pow-worker.js',
});
