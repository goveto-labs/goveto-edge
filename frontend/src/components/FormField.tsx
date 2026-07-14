import type { ReactNode } from 'react';

interface FormFieldProps {
    label: string;
    children: ReactNode;
    htmlFor?: string;
    hint?: string;
    error?: string;
    required?: boolean;
    className?: string;
}

export function FormField({
    label,
    children,
    htmlFor,
    hint,
    error,
    required,
    className = '',
}: FormFieldProps) {
    return (
        <div className={`flex flex-col gap-1.5 ${className}`}>
            <label className='text-sm font-medium text-foreground' htmlFor={htmlFor}>
                {label}
                {required && <span className='ml-0.5 text-danger'>*</span>}
            </label>
            {children}
            {error && <p className='text-xs text-danger'>{error}</p>}
            {hint && !error && <p className='text-xs text-muted'>{hint}</p>}
        </div>
    );
}

interface FormErrorProps {
    message: string;
}

export function FormError({ message }: FormErrorProps) {
    return (
        <div
            className='rounded-lg border border-danger/20 bg-danger/10 px-4 py-3 text-sm text-danger-foreground'
            role='alert'
        >
            {message}
        </div>
    );
}
