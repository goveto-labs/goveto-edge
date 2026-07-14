import type { LoginRequest, RegisterRequest, RegistrationConfig, User } from './types.ts';

import { get, post } from './client.ts';

export const authApi = {
    login: (payload: LoginRequest) => post<User>('/auth/login', payload),
    register: (payload: RegisterRequest) => post<User>('/auth/register', payload),
    me: () => get<User>('/auth/me'),
    registrationConfig: () => get<RegistrationConfig>('/auth/registration-config'),
};
