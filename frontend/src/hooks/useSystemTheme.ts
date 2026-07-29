import { useLayoutEffect } from 'react';

export function useSystemTheme() {
    useLayoutEffect(() => {
        const root = document.documentElement;
        const colorScheme = window.matchMedia('(prefers-color-scheme: dark)');
        const previousTheme = root.getAttribute('data-theme');
        const applyTheme = () =>
            root.setAttribute('data-theme', colorScheme.matches ? 'dark' : 'light');

        applyTheme();
        colorScheme.addEventListener('change', applyTheme);
        return () => {
            colorScheme.removeEventListener('change', applyTheme);
            if (previousTheme) root.setAttribute('data-theme', previousTheme);
            else root.removeAttribute('data-theme');
        };
    }, []);
}
