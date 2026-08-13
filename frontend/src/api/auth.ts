import type {
    AuthMethods,
    ExternalAuthStartResponse,
    LoginRequest,
    RecoveryCodesResponse,
    RegisterRequest,
    RegistrationConfig,
    TOTPMutationRequest,
    TOTPSetup,
    User,
} from './types.ts';

import { del, get, post } from './client.ts';

export const authApi = {
    methods: () => get<AuthMethods>('/auth/methods'),
    linkExternalProvider: (providerId: string) =>
        post<ExternalAuthStartResponse>(
            `/auth/providers/${encodeURIComponent(providerId)}/link?return_to=${encodeURIComponent('/settings?external_link=success')}`
        ),
    login: (payload: LoginRequest) => post<User>('/auth/login', payload),
    register: (payload: RegisterRequest) => post<User>('/auth/register', payload),
    me: () => get<User>('/auth/me'),
    registrationConfig: () => get<RegistrationConfig>('/auth/registration-config'),
    setupTOTP: () => post<TOTPSetup>('/auth/totp/setup'),
    enableTOTP: (payload: TOTPMutationRequest) =>
        post<RecoveryCodesResponse>('/auth/totp/enable', payload),
    regenerateTOTPRecoveryCodes: (payload: TOTPMutationRequest) =>
        post<RecoveryCodesResponse>('/auth/totp/recovery-codes', payload),
    disableTOTP: (payload: TOTPMutationRequest) => del<void>('/auth/totp', { data: payload }),
    logout: () => post<void>('/auth/logout'),
};
