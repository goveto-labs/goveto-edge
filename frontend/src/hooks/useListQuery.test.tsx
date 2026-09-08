import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, test, vi } from 'vitest';

import { useListQuery } from './useListQuery.ts';
import { enumField, integerField, stringField } from '@/utils/listQuery.ts';

const schema = {
    q: stringField(),
    status: enumField(['', 'FAILED'] as const, ''),
    page: integerField(1, { min: 1 }),
};

function Probe() {
    const query = useListQuery(schema, { searchKey: 'q', debounceMs: 50 });
    const location = useLocation();
    return (
        <div>
            <div data-testid='search'>{location.search}</div>
            <div data-testid='status'>{query.values.status || 'all'}</div>
            <input
                aria-label='Search jobs'
                value={query.searchInput}
                onChange={(event) => query.setSearchInput(event.target.value)}
            />
            <button
                type='button'
                onClick={() => query.replace({ status: 'FAILED' }, { resetPage: true })}
            >
                Failed only
            </button>
        </div>
    );
}

function renderProbe(path: string) {
    return render(
        <MemoryRouter initialEntries={[path]}>
            <Routes>
                <Route element={<Probe />} path='/jobs' />
            </Routes>
        </MemoryRouter>
    );
}

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('useListQuery', () => {
    test('reads filters from the current URL', () => {
        renderProbe('/jobs?status=FAILED&page=3');
        expect(screen.getByTestId('status').textContent).toBe('FAILED');
        expect(screen.getByTestId('search').textContent).toBe('?status=FAILED&page=3');
    });

    test('writes filter changes and resets page while keeping the search query', async () => {
        const user = userEvent.setup();
        renderProbe('/jobs?q=origin&page=4');
        await user.click(screen.getByRole('button', { name: 'Failed only' }));
        expect(screen.getByTestId('search').textContent).toBe('?q=origin&status=FAILED');
    });

    test('debounces search input into a shareable query parameter', async () => {
        vi.useFakeTimers();
        renderProbe('/jobs');
        fireEvent.change(screen.getByLabelText('Search jobs'), { target: { value: 'origin' } });
        expect(screen.getByTestId('search').textContent).toBe('');
        await act(async () => {
            vi.advanceTimersByTime(50);
        });
        expect(screen.getByTestId('search').textContent).toBe('?q=origin');
    });

    test('preserves pending search input when another filter changes', async () => {
        vi.useFakeTimers();
        renderProbe('/jobs?page=4');

        const searchInput = screen.getByLabelText('Search jobs') as HTMLInputElement;
        fireEvent.change(searchInput, { target: { value: 'origin' } });
        fireEvent.click(screen.getByRole('button', { name: 'Failed only' }));

        expect(searchInput.value).toBe('origin');
        expect(screen.getByTestId('search').textContent).toBe('?status=FAILED');

        await act(async () => {
            vi.advanceTimersByTime(50);
        });
        expect(screen.getByTestId('search').textContent).toBe('?q=origin&status=FAILED');
    });
});
