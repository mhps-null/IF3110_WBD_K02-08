import { apiRequest } from './client';

export type JobStatus =
  | 'queued'
  | 'retrying'
  | 'running'
  | 'compensating'
  | 'uncertain'
  | 'success'
  | 'failed'
  | 'partial'
  | 'partially_reverted'
  | 'reverted'
  | 'cancelled'
  | 'rejected';

export interface MigrationJob {
  id: number;
  request_id: number;
  db_id: number;
  db_name?: string;
  migration_id: number;
  revision_id: number;
  title: string;
  action: 'up' | 'rollback' | 'forward';
  request_status: string;
  mode: 'normal' | 'batch';
  op: 'apply' | 'compensate';
  status: JobStatus;
  batch_table?: string;
  batch_column?: string;
  batch_size?: number;
  source_job_id?: number | null;
  scope_from?: number | null;
  scope_to?: number | null;
  total_rows?: number | null;
  rows_done: number;
  batches_done: number;
  rows_reverted: number;
  batches_reverted: number;
  last_cursor?: number | null;
  max_attempts: number;
  retry_count: number;
  attempts: number;
  deliveries: number;
  recoveries: number;
  next_run_at: string;
  lease_owner?: string;
  lease_expires_at?: string | null;
  heartbeat_at?: string | null;
  cancel_requested: boolean;
  last_error?: string;
  held: boolean;
  correlation_id?: string;
  queued_at: string;
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
  updated_at: string;
}

export interface JobBatch {
  batch_seq: number;
  cursor_from: number;
  cursor_to: number;
  rows_affected: number;
  status: 'committed' | 'reverted';
  committed_at: string;
  reverted_at?: string | null;
}

export interface JobEvent {
  id: number;
  event: string;
  status: string;
  message?: string;
  worker_id?: string;
  attempt?: number | null;
  batch_seq?: number | null;
  created_at: string;
}

export interface JobDetail {
  job: MigrationJob;
  batches: JobBatch[];
  events: JobEvent[];
  actions: { resume: boolean; cancel: boolean; compensate: boolean };
}

export interface ExecuteOptions {
  revision_id: number;
  mode?: 'normal' | 'batch';
  batch_table?: string;
  batch_column?: string;
  batch_size?: number;
  max_attempts?: number;
}

/** Statuses for which the worker will still change the job without user action. */
export const ACTIVE_STATUSES: JobStatus[] = ['queued', 'retrying', 'running', 'compensating', 'uncertain'];

export const isActive = (s: JobStatus) => ACTIVE_STATUSES.includes(s);

export const jobsApi = {
  list: (params: { db_id?: number; status?: string } = {}) => {
    const q = new URLSearchParams();
    if (params.db_id) q.set('db_id', String(params.db_id));
    if (params.status) q.set('status', params.status);
    const qs = q.toString();
    return apiRequest<{ items: MigrationJob[] }>(`/api/v2/jobs${qs ? `?${qs}` : ''}`);
  },
  detail: (id: number) => apiRequest<JobDetail>(`/api/v2/jobs/${id}`),
  execute: (db: number, request: number, opts: ExecuteOptions) =>
    apiRequest<{ job_id: number; request_id: number; status: JobStatus; job: MigrationJob }>(
      `/api/v2/databases/${db}/requests/${request}/execute`,
      { method: 'POST', body: opts },
    ),
  resume: (id: number) => apiRequest<{ job: MigrationJob }>(`/api/v2/jobs/${id}/resume`, { method: 'POST' }),
  cancel: (id: number) => apiRequest<{ job: MigrationJob }>(`/api/v2/jobs/${id}/cancel`, { method: 'POST' }),
  compensate: (id: number) => apiRequest<{ job: MigrationJob }>(`/api/v2/jobs/${id}/compensate`, { method: 'POST' }),
};
