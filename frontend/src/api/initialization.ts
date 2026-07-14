import type { InitializationStatus, InitializeRequest, User } from './types.ts';

import { get, post } from './client.ts';

export const initializationApi = {
    status: () => get<InitializationStatus>('/init/status'),
    initialize: (payload: InitializeRequest) => post<User>('/init', payload),
};
