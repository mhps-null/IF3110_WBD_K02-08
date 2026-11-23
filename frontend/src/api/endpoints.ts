import { apiRequest, clearCachedCSRFToken, setCachedCSRFToken } from './client';
import type {
  AccessMatrixResponse,
  AccessibleDatabase,
  AdminGroup,
  AdminUser,
  AuditPage,
  AuthMeResponse,
  DatabaseAccessGroupsResponse,
  DBConnection,
  GroupDetailResponse,
  MigrationRecord,
  PoolStat,
  Principal,
  SchemaResponse,
  TableDataResponse,
} from '../types/api';

export const authApi = {
  async login(username: string, password: string): Promise<{ user: Principal; csrf_token?: string }> {
    const res = await apiRequest<{ user: Principal; csrf_token?: string }>('/api/auth/login', {
      method: 'POST',
      body: { username, password },
      skipAuthRedirect: true,
    });
    setCachedCSRFToken(res.csrf_token);
    return res;
  },
  async logout(): Promise<void> {
    await apiRequest('/api/auth/logout', { method: 'POST', skipAuthRedirect: true });
    clearCachedCSRFToken();
  },
  async me(): Promise<AuthMeResponse> {
    const res = await apiRequest<AuthMeResponse>('/api/auth/me', { skipAuthRedirect: true });
    setCachedCSRFToken(res.csrf_token);
    return res;
  },
};

export const adminApi = {
  listUsers: () => apiRequest<{ users: AdminUser[] }>('/api/admin/users'),
  createUser: (username: string, password: string) =>
    apiRequest<{ id: number; username: string; message: string }>('/api/admin/users', {
      method: 'POST',
      body: { username, password },
    }),
  deactivateUser: (id: number) =>
    apiRequest<{ message: string }>(`/api/admin/users/${id}/deactivate`, { method: 'POST' }),
  activateUser: (id: number) =>
    apiRequest<{ message: string }>(`/api/admin/users/${id}/activate`, { method: 'POST' }),
  resetPassword: (id: number, password: string) =>
    apiRequest<{ message: string }>(`/api/admin/users/${id}/reset-password`, {
      method: 'POST',
      body: { password },
    }),
  toggleAdmin: (id: number, isAdmin: boolean) =>
    apiRequest<{ message: string }>(`/api/admin/users/${id}/toggle-admin`, {
      method: 'POST',
      body: { is_admin: isAdmin },
    }),

  listGroups: () => apiRequest<{ groups: AdminGroup[] }>('/api/admin/groups'),
  createGroup: (name: string, description: string) =>
    apiRequest<{ id: number; message: string }>('/api/admin/groups', {
      method: 'POST',
      body: { name, description },
    }),
  getGroup: (id: number) => apiRequest<GroupDetailResponse>(`/api/admin/groups/${id}`),
  updateGroup: (id: number, name: string, description: string) =>
    apiRequest<{ message: string }>(`/api/admin/groups/${id}`, {
      method: 'PUT',
      body: { name, description },
    }),
  deleteGroup: (id: number) =>
    apiRequest<{ message: string }>(`/api/admin/groups/${id}`, { method: 'DELETE' }),
  addMember: (groupId: number, userId: number) =>
    apiRequest<{ message: string }>(`/api/admin/groups/${groupId}/members`, {
      method: 'POST',
      body: { user_id: userId },
    }),
  removeMember: (groupId: number, userId: number) =>
    apiRequest<{ message: string }>(`/api/admin/groups/${groupId}/members/${userId}`, {
      method: 'DELETE',
    }),
  setGroupLeader: (groupId: number, leaderUserId: number | null) =>
    apiRequest<{ message: string; group?: AdminGroup }>(`/api/admin/groups/${groupId}/leader`, {
      method: 'PUT',
      body: { leader_user_id: leaderUserId },
    }),

  getAccessMatrix: () => apiRequest<AccessMatrixResponse>('/api/admin/access'),
  setAccess: (groupId: number, dbId: number, level: number) =>
    apiRequest<{ message: string }>('/api/admin/access', {
      method: 'PUT',
      body: { group_id: groupId, db_id: dbId, level },
    }),

  getDatabaseAccessGroups: (dbId: number) =>
    apiRequest<DatabaseAccessGroupsResponse>(`/api/databases/${dbId}/access-groups`),
};

export interface ConnectionPayload {
  id?: number;
  name: string;
  engine: 'postgres' | 'mysql' | 'mariadb';
  host: string;
  port: number;
  database_name: string;
  username: string;
  password: string;
  ssl_mode: string;
}

export const connectionsApi = {
  list: () => apiRequest<{ connections: DBConnection[] }>('/api/admin/connections'),
  create: (payload: ConnectionPayload) =>
    apiRequest<{ connection: DBConnection; message: string }>('/api/admin/connections', {
      method: 'POST',
      body: payload,
    }),
  update: (id: number, payload: ConnectionPayload) =>
    apiRequest<{ connection: DBConnection; message: string }>(`/api/admin/connections/${id}`, {
      method: 'PUT',
      body: payload,
    }),
  remove: (id: number) =>
    apiRequest<{ message: string }>(`/api/admin/connections/${id}`, { method: 'DELETE' }),
  test: (payload: Omit<ConnectionPayload, 'name'> & { name?: string }) =>
    apiRequest<{ success: boolean; message: string }>('/api/admin/connections/test', {
      method: 'POST',
      body: payload,
    }),
  poolStats: () => apiRequest<{ pools: PoolStat[] }>('/api/admin/connections/pool-stats'),
};

export const viewerApi = {
  listDatabases: () => apiRequest<{ databases: AccessibleDatabase[] }>('/api/databases'),
  getSchema: (dbId: number) => apiRequest<SchemaResponse>(`/api/databases/${dbId}/schema`),
  getTableData: (
    dbId: number,
    table: string,
    params: { schema?: string; page?: number; pageSize?: number; orderBy?: string; desc?: boolean },
  ) => {
    const qs = new URLSearchParams();
    if (params.schema) qs.set('schema', params.schema);
    if (params.page) qs.set('page', String(params.page));
    if (params.pageSize) qs.set('page_size', String(params.pageSize));
    if (params.orderBy) qs.set('order_by', params.orderBy);
    if (params.desc) qs.set('desc', 'true');
    const query = qs.toString();
    return apiRequest<TableDataResponse>(
      `/api/databases/${dbId}/tables/${encodeURIComponent(table)}/data${query ? `?${query}` : ''}`,
    );
  },
};

export const auditApi = {
  list: (params: { actor?: string; event?: string; database?: string; status?: string; page?: number; pageSize?: number }) => {
    const qs = new URLSearchParams();
    if (params.actor) qs.set('actor', params.actor);
    if (params.event) qs.set('event', params.event);
    if (params.database) qs.set('database', params.database);
    if (params.status) qs.set('status', params.status);
    if (params.page) qs.set('page', String(params.page));
    if (params.pageSize) qs.set('page_size', String(params.pageSize));
    const query = qs.toString();
    return apiRequest<AuditPage>(`/api/audit-logs${query ? `?${query}` : ''}`);
  },
};

export const migrationsApi = {
  listByDatabase: (dbId: number) =>
    apiRequest<{ db_id: number; migrations: MigrationRecord[] }>(`/api/databases/${dbId}/migrations`),
};
