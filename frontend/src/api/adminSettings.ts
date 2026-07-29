import type { AdminSettings, UpdateAdminSettings } from './types.ts';

import { get, put } from './client.ts';

export const adminSettingsApi = {
    get: () => get<AdminSettings>('/admin/settings'),
    update: (payload: UpdateAdminSettings) => put<AdminSettings>('/admin/settings', payload),
};
