import type { AxiosError, AxiosRequestConfig, AxiosResponse } from 'axios';

import axios from 'axios';

export const API_BASE = '/api/v1';

export interface ApiEnvelope<T = unknown> {
    code: string;
    msg?: string;
    data?: T;
}

export class ApiError extends Error {
    status: number;
    code: string;
    data: unknown;

    constructor(message: string, status: number, data: unknown, code = 'error') {
        super(message);
        this.status = status;
        this.code = code;
        this.data = data;
    }
}

function isEnvelope(value: unknown): value is ApiEnvelope {
    return typeof value === 'object' && value !== null && 'code' in value;
}

function extractMessage(error: AxiosError<unknown>): string {
    const data = error.response?.data;
    if (isEnvelope(data) && data.msg) {
        return data.msg;
    }
    if (typeof data === 'object' && data !== null && 'message' in data) {
        return String((data as { message?: string }).message);
    }
    return error.message;
}

function extractCode(error: AxiosError<unknown>): string {
    const data = error.response?.data;
    if (isEnvelope(data) && data.code) {
        return data.code;
    }
    return 'error';
}

function unwrapData<T>(response: AxiosResponse<unknown>): T {
    const body = response.data;
    if (isEnvelope(body)) {
        if (body.code !== 'ok') {
            throw new ApiError(body.msg || body.code, response.status, body, body.code);
        }
        return body.data as T;
    }
    return body as T;
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
                new ApiError(
                    extractMessage(error),
                    error.response.status,
                    error.response.data,
                    extractCode(error)
                )
            );
        }
        return Promise.reject(error);
    }
);

export async function get<T>(path: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await apiClient.get(path, config);
    return unwrapData<T>(response);
}

export async function post<T>(
    path: string,
    body?: unknown,
    config?: AxiosRequestConfig
): Promise<T> {
    const response = await apiClient.post(path, body, config);
    return unwrapData<T>(response);
}

export async function put<T>(
    path: string,
    body?: unknown,
    config?: AxiosRequestConfig
): Promise<T> {
    const response = await apiClient.put(path, body, config);
    return unwrapData<T>(response);
}

export async function patch<T>(
    path: string,
    body?: unknown,
    config?: AxiosRequestConfig
): Promise<T> {
    const response = await apiClient.patch(path, body, config);
    return unwrapData<T>(response);
}

export async function del<T = void>(path: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await apiClient.delete(path, config);
    return unwrapData<T>(response);
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
