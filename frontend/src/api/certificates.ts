import type { Certificate, CreateCertificateRequest } from './types.ts';

import { get, post } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const certificatesApi = (clusterId: string) => ({
    list: () => get<Certificate[]>(clusterPath(clusterId, '/certificates')),
    create: (payload: CreateCertificateRequest) =>
        post<Certificate>(clusterPath(clusterId, '/certificates'), payload),
});
