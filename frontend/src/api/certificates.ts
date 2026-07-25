import type {
    Certificate,
    CertificateJob,
    CreateACMECertificateRequest,
    CreateACMECertificateResponse,
    CreateCertificateRequest,
} from './types.ts';

import { del, get, patch, post, put } from './client.ts';

function clusterPath(clusterId: string, path: string) {
    return `/clusters/${clusterId}${path}`;
}

export const certificatesApi = (clusterId: string) => ({
    list: () => get<Certificate[]>(clusterPath(clusterId, '/certificates')),
    create: (payload: CreateCertificateRequest) =>
        post<Certificate>(clusterPath(clusterId, '/certificates'), payload),
    createACME: (payload: CreateACMECertificateRequest) =>
        post<CreateACMECertificateResponse>(clusterPath(clusterId, '/certificates/acme'), payload),
    update: (
        certificateId: string,
        payload: { name?: string; auto_renew?: boolean; renew_before_days?: number }
    ) => patch<Certificate>(clusterPath(clusterId, `/certificates/${certificateId}`), payload),
    replaceMaterial: (certificateId: string, payload: CreateCertificateRequest) =>
        put<Certificate>(
            clusterPath(clusterId, `/certificates/${certificateId}/material`),
            payload
        ),
    renew: (certificateId: string) =>
        post<CertificateJob>(clusterPath(clusterId, `/certificates/${certificateId}/renew`)),
    reissue: (certificateId: string) =>
        post<CertificateJob>(clusterPath(clusterId, `/certificates/${certificateId}/reissue`)),
    publish: (certificateId: string) =>
        post<CertificateJob>(clusterPath(clusterId, `/certificates/${certificateId}/publish`)),
    remove: (certificateId: string) =>
        del(clusterPath(clusterId, `/certificates/${certificateId}`)),
});
