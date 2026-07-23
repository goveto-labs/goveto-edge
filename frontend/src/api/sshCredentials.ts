import type { SSHCredential, SSHCredentialNode, SSHCredentialWriteRequest } from './types.ts';

import { del, get, post, put } from './client.ts';

function path(clusterId: string, suffix = '') {
    return `/clusters/${clusterId}/ssh-credentials${suffix}`;
}

export const sshCredentialsApi = (clusterId: string) => ({
    list: () => get<SSHCredential[]>(path(clusterId)),
    create: (payload: SSHCredentialWriteRequest) => post<SSHCredential>(path(clusterId), payload),
    update: (credentialId: string, payload: SSHCredentialWriteRequest) =>
        put<SSHCredential>(path(clusterId, `/${credentialId}`), payload),
    delete: (credentialId: string) => del<void>(path(clusterId, `/${credentialId}`)),
    nodes: (credentialId: string) =>
        get<SSHCredentialNode[]>(path(clusterId, `/${credentialId}/nodes`)),
});
