import type { ReactNode } from 'react';

import {
    createContext,
    createElement,
    useCallback,
    useContext,
    useEffect,
    useMemo,
    useRef,
    useState,
} from 'react';

import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';

interface PendingAction {
    action: () => void | Promise<void>;
    description?: ReactNode;
}

interface UnsavedChangesContextValue {
    hasUnsavedChanges: boolean;
    requestAction: (action: PendingAction['action'], description?: ReactNode) => boolean;
    setDirty: (key: string, dirty: boolean, onDiscard?: () => void) => void;
}

const UnsavedChangesContext = createContext<UnsavedChangesContextValue | null>(null);

export function UnsavedChangesProvider({ children }: { children: ReactNode }) {
    const dirtyEntries = useRef(new Map<string, (() => void) | undefined>());
    const [, setDirtyVersion] = useState(0);
    const [pending, setPending] = useState<PendingAction | null>(null);
    const hasUnsavedChanges = dirtyEntries.current.size > 0;

    const setDirty = useCallback((key: string, dirty: boolean, onDiscard?: () => void) => {
        const changed = dirty ? !dirtyEntries.current.has(key) : dirtyEntries.current.has(key);
        if (dirty) dirtyEntries.current.set(key, onDiscard);
        else dirtyEntries.current.delete(key);
        if (changed) setDirtyVersion((value) => value + 1);
    }, []);

    const requestAction = useCallback(
        (action: PendingAction['action'], description?: ReactNode) => {
            if (dirtyEntries.current.size === 0) {
                void action();
                return true;
            }
            setPending({ action, description });
            return false;
        },
        []
    );

    useEffect(() => {
        const handleBeforeUnload = (event: BeforeUnloadEvent) => {
            if (dirtyEntries.current.size === 0) return;
            event.preventDefault();
            event.returnValue = '';
        };
        window.addEventListener('beforeunload', handleBeforeUnload);
        return () => {
            window.removeEventListener('beforeunload', handleBeforeUnload);
        };
    }, []);

    const value = useMemo(
        () => ({ hasUnsavedChanges, requestAction, setDirty }),
        [hasUnsavedChanges, requestAction, setDirty]
    );

    return createElement(
        UnsavedChangesContext.Provider,
        { value },
        children,
        <ConfirmDialog
            danger
            confirmLabel='Leave without saving'
            description={
                pending?.description ??
                'Unsaved policy changes will be discarded. This action cannot be recovered.'
            }
            isOpen={pending !== null}
            title='Discard unsaved changes?'
            onConfirm={() => {
                const action = pending?.action;
                for (const discard of dirtyEntries.current.values()) discard?.();
                dirtyEntries.current.clear();
                setDirtyVersion((version) => version + 1);
                setPending(null);
                if (action) void action();
            }}
            onOpenChange={(open) => {
                if (!open) setPending(null);
            }}
        />
    );
}

export function useUnsavedChanges(isDirty?: boolean, key = 'default', onDiscard?: () => void) {
    const context = useContext(UnsavedChangesContext);
    if (!context) throw new Error('useUnsavedChanges must be used within UnsavedChangesProvider');
    const { setDirty } = context;
    const discardRef = useRef(onDiscard);
    discardRef.current = onDiscard;
    useEffect(() => {
        if (isDirty === undefined) return;
        setDirty(key, isDirty, () => discardRef.current?.());
        return () => setDirty(key, false);
    }, [isDirty, key, setDirty]);
    return context;
}
