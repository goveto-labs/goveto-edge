import type { AxiosError, AxiosRequestConfig } from 'axios';

import axios from 'axios';

export const API_BASE = '/api/v1';

export class ApiError extends Error {
    status: number;
    data: unknown;

    constructor(message: string, status: number, data: unknown) {
        super(message);
        this.status = status;
        this.data = data;
    }
}

function extractMessage(error: AxiosError<unknown>): string {
    const data = error.response?.data;
    if (typeof data === 'object' && data !== null && 'message' in data) {
        return String((data as { message?: string }).message);
    }
    return error.message;
}

export const apiClient = axios.create({
    baseURL: API_BASE,
    withCredentials: true,
    headers: {
        'Content-Type': 'application/json',
    },
});

apiClient.interceptors.response.use(
    (response) => response,
    (error: AxiosError<unknown>) => {
        if (error.response) {
            return Promise.reject(
                new ApiError(extractMessage(error), error.response.status, error.response.data)
            );
        }
        return Promise.reject(error);
    }
);

export async function get<T>(path: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await apiClient.get<T>(path, config);
    return response.data;
}

export async function post<T>(
    path: string,
    body?: unknown,
    config?: AxiosRequestConfig
): Promise<T> {
    const response = await apiClient.post<T>(path, body, config);
    return response.data;
}

export async function put<T>(
    path: string,
    body?: unknown,
    config?: AxiosRequestConfig
): Promise<T> {
    const response = await apiClient.put<T>(path, body, config);
    return response.data;
}

export async function patch<T>(
    path: string,
    body?: unknown,
    config?: AxiosRequestConfig
): Promise<T> {
    const response = await apiClient.patch<T>(path, body, config);
    return response.data;
}

export async function del<T = void>(path: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await apiClient.delete<T>(path, config);
    return response.data;
}

export function buildQuery(params: Record<string, string | number | boolean | undefined>): string {
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
        if (value === undefined || value === '') continue;
        search.set(key, String(value));
    }
    const query = search.toString();
    return query ? `?${query}` : '';
}
