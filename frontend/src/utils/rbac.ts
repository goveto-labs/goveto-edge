import type { ClusterRole } from '@/api/types.ts';

export function canOperateCluster(role?: ClusterRole) {
    return role === 'OPERATOR' || role === 'OWNER' || role === 'ADMIN';
}

export function canManageCluster(role?: ClusterRole) {
    return role === 'OWNER' || role === 'ADMIN';
}
