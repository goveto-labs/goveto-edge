import { fileURLToPath } from 'node:url';

import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// https://vitejs.dev/config/
export default defineConfig({
    resolve: {
        alias: {
            '@': fileURLToPath(new URL('./src', import.meta.url)),
        },
    },
    plugins: [react(), tailwindcss()],
    build: {
        rolldownOptions: {
            output: {
                codeSplitting: {
                    groups: [
                        {
                            name: 'react-vendor',
                            test: /node_modules\/(?:react|react-dom|react-router|react-router-dom)\//,
                        },
                        {
                            name: 'heroui-vendor',
                            test: /node_modules\/(?:@heroui|tailwind-variants)\//,
                        },
                        {
                            name: 'http-vendor',
                            test: /node_modules\/axios\//,
                        },
                    ],
                },
            },
        },
    },
    server: {
        proxy: {
            '/api': {
                target: 'http://localhost:8080',
                changeOrigin: false,
                ws: true,
            },
        },
    },
});
