import type {
    CreateNodeRequest,
    Node,
    NodeAddress,
    NodeCacheConfig,
    NodeCacheUpdateResponse,
    NodeDNSLinesResponse,
    NodeInstallationInfo,
    NodeSSH,
    NodeSSHHostKey,
    NodeSSHHostKeyPreview,
    NodeStatusResponse,
    SSHConnectionTestResponse,
} from './types.ts';

import { ApiError, del, get, post, put } from './client.ts';
import { SSH_HOST_KEY_CHANGED_CODE, SSH_HOST_KEY_REQUIRED_CODE } from './types.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const nodesApi = (clusterId: string) => ({
    list: () => get<Node[]>(clusterPath(clusterId, '/nodes')),
    get: (nodeId: string) => get<Node>(clusterPath(clusterId, `/nodes/${nodeId}`)),
    create: (payload: CreateNodeRequest) => post<Node>(clusterPath(clusterId, '/nodes'), payload),
    testConnection: (ssh: NodeSSH) =>
        post<SSHConnectionTestResponse>(clusterPath(clusterId, '/nodes/test-connection'), { ssh }),
    reinstall: (nodeId: string, ssh: NodeSSH, force = false) =>
        post<Node>(clusterPath(clusterId, `/nodes/${nodeId}/reinstall`), { ssh, force }),
    previewSSHHostKey: (nodeId: string) =>
        post<NodeSSHHostKeyPreview>(
            clusterPath(clusterId, `/nodes/${nodeId}/ssh-host-key/preview`)
        ),
    trustSSHHostKey: (nodeId: string) =>
        post<NodeSSHHostKey>(clusterPath(clusterId, `/nodes/${nodeId}/ssh-host-key/trust`)),
    installation: (nodeId: string) =>
        get<NodeInstallationInfo>(clusterPath(clusterId, `/nodes/${nodeId}/installation`)),
    initializeInstallation: (nodeId: string) =>
        post<NodeStatusResponse>(
            clusterPath(clusterId, `/nodes/${nodeId}/installation/initialize`),
            {}
        ),
    installationArtifactUrl: (nodeId: string, artifact: string) =>
        `/api/v1${clusterPath(clusterId, `/nodes/${nodeId}/installation/${artifact}`)}`,
    delete: (nodeId: string) => del<void>(clusterPath(clusterId, `/nodes/${nodeId}`)),
    updateDNSLines: (nodeId: string, dnsLineIds: string[]) =>
        put<NodeDNSLinesResponse>(clusterPath(clusterId, `/nodes/${nodeId}/dns-lines`), {
            dns_line_ids: dnsLineIds,
        }),
    enable: (nodeId: string) =>
        post<NodeStatusResponse>(clusterPath(clusterId, `/nodes/${nodeId}/enable`)),
    disable: (nodeId: string) =>
        post<NodeStatusResponse>(clusterPath(clusterId, `/nodes/${nodeId}/disable`)),

    getCacheConfig: (nodeId: string) =>
        get<NodeCacheConfig>(clusterPath(clusterId, `/nodes/${nodeId}/cache-config`)),
    updateCacheConfig: (nodeId: string, payload: NodeCacheConfig) =>
        put<NodeCacheUpdateResponse>(
            clusterPath(clusterId, `/nodes/${nodeId}/cache-config`),
            payload
        ),

    addAddress: (nodeId: string, payload: { address: string }) =>
        post<NodeAddress>(clusterPath(clusterId, `/nodes/${nodeId}/addresses`), payload),
    updateAddress: (nodeId: string, addressId: string, payload: { address: string }) =>
        put<NodeAddress>(
            clusterPath(clusterId, `/nodes/${nodeId}/addresses/${addressId}`),
            payload
        ),
    deleteAddress: (nodeId: string, addressId: string) =>
        del<void>(clusterPath(clusterId, `/nodes/${nodeId}/addresses/${addressId}`)),
});

export function isSSHHostKeyApiError(error: unknown): boolean {
    return (
        error instanceof ApiError &&
        (error.code === SSH_HOST_KEY_CHANGED_CODE || error.code === SSH_HOST_KEY_REQUIRED_CODE)
    );
}

/** Detect host-key operator action needed from install_error text written by the worker. */
export function installErrorNeedsHostKeyTrust(message?: string | null): boolean {
    if (!message) return false;
    return message.includes('SSH host key changed') || message.includes('no pinned SSH host key');
}
