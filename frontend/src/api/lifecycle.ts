import { apiRequest } from './client';

export interface LifecycleRequest {
  id: number; migration_id: number; revision_id: number; db_id: number;
  action: 'up' | 'rollback' | 'forward'; requested_by: number;
  status: string; target_execution_id?: number | null;
}

export interface LifecycleApproval {
  group_id: number; leader_id: number | null; approved_by: number | null;
  decision: 'approved' | 'rejected' | null; decided_by: number | null; decided_at: string | null;
}
export interface LifecycleApprovalDecision {
  id: number; revision_id: number; group_id: number; leader_id: number | null;
  actor_id: number; decision: 'approved' | 'rejected'; decided_at: string;
}

export interface LifecycleDefinition {
  db_id: number; title: string; sql_up: string; sql_down: string; irreversible_reason: string;
}

export interface LifecycleExecution {
  execution_id: number; attempt_id: number; request_id: number; migration_id: number; revision_id: number;
  user_id: number; action: string; status: string; sql_snapshot: string;
  rollback_of_execution_id?: number | null; attempt: number; executed_at: string;
  error_message?: string | null;
}

const dbURL = (id: number) => `/api/v2/databases/${id}`;
export const lifecycleApi = {
  list: (db: number) => apiRequest<{items: LifecycleRequest[]}>(`${dbURL(db)}/requests`),
  history: (db: number, hide: boolean) => apiRequest<{items: LifecycleExecution[]}>(`${dbURL(db)}/history?hide_rollback=${hide}`),
  create: (db: number, data: Omit<LifecycleDefinition, 'db_id'>) => apiRequest<LifecycleRequest>(`${dbURL(db)}/migrations`, {method: 'POST', body: data}),
  action: (db: number, migration: number, action: 'rollback' | 'forward') => apiRequest<LifecycleRequest>(`${dbURL(db)}/migrations/${migration}/actions`, {method: 'POST', body: {action}}),
  revise: (db: number, id: number, data: Omit<LifecycleDefinition, 'db_id'>) => apiRequest<LifecycleRequest>(`${dbURL(db)}/requests/${id}`, {method: 'PATCH', body: data}),
  detail: (db: number, id: number) => apiRequest<{
    request: LifecycleRequest; approvals: LifecycleApproval[];
    approval_history: LifecycleApprovalDecision[]; definition: LifecycleDefinition;
  }>(`${dbURL(db)}/requests/${id}`),
  validate: (db: number, id: number) => apiRequest<{valid: boolean}>(`${dbURL(db)}/requests/${id}/validate`, {method: 'POST'}),
  submit: (db: number, id: number) => apiRequest<LifecycleRequest>(`${dbURL(db)}/requests/${id}/submit`, {method: 'POST'}),
  refreshReview: (db: number, id: number) => apiRequest<LifecycleRequest>(`${dbURL(db)}/requests/${id}/refresh-review`, {method: 'POST'}),
  decide: (db: number, id: number, decision: 'approve' | 'reject') => apiRequest<LifecycleRequest>(`${dbURL(db)}/requests/${id}/${decision}`, {method: 'POST'}),
  queue: (db: number, id: number) => apiRequest<{job_id: number; status: string}>(`${dbURL(db)}/requests/${id}/queue`, {method: 'POST'}),
  inbox: () => apiRequest<{items: LifecycleRequest[]}>('/api/v2/review-inbox'),
};
