import type { ReactNode } from 'react';

interface FormRowProps {
    label: string;
    children: ReactNode;
    htmlFor?: string;
    hint?: string;
    error?: string;
    required?: boolean;
}

export function FormRow({ label, children, htmlFor, hint, error, required }: FormRowProps) {
    return (
        <div className='grid grid-cols-1 gap-2 border-b border-border py-4 last:border-0 grid-cols-[5.5rem_1fr] md:items-start md:gap-6'>
            <label className='text-sm font-medium md:pt-2 md:text-right' htmlFor={htmlFor}>
                {label}
                {required && <span className='ml-0.5 text-danger'>*</span>}
            </label>
            <div className='min-w-0 space-y-1.5'>
                {children}
                {error && <p className='text-xs text-danger'>{error}</p>}
                {hint && !error && <p className='text-xs text-muted'>{hint}</p>}
            </div>
        </div>
    );
}
