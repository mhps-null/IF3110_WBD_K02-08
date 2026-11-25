import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { AppRoutes } from '../App';
import { AuthProvider } from '../context/AuthContext';
import { ToastProvider } from '../context/ToastContext';

const me = {
  user: { id: 2, username: 'alice', is_platform_admin: false },
  groups: [{ group_id: 1, group_name: 'Backend', description: '', is_leader: true }],
  accessible_databases: [{ db_id: 1, name: 'target_pg', engine: 'postgres', level: 2, level_label: 'View + Edit' }],
  csrf_token: 'csrf-123',
};

const job = {
  id: 5, request_id: 9, db_id: 1, db_name: 'target_pg', migration_id: 3, revision_id: 4, title: 'Backfill status',
  action: 'up', request_status: 'failed', mode: 'batch', op: 'apply', status: 'partial', batch_table: 'big',
  batch_column: 'id', batch_size: 100, scope_from: 0, scope_to: 1000, total_rows: 1000, rows_done: 300,
  batches_done: 3, rows_reverted: 0, batches_reverted: 0, last_cursor: 300, max_attempts: 3, retry_count: 0,
  attempts: 1, deliveries: 1, recoveries: 0, next_run_at: '2026-11-25T00:00:00Z', cancel_requested: false,
  last_error: 'batch 4 failed and was rolled back', held: true, queued_at: '2026-11-25T00:00:00Z',
  created_at: '2026-11-25T00:00:00Z', updated_at: '2026-11-25T00:00:00Z',
};

function setup(routes: Record<string, unknown>) {
  const calls: { url: string; method: string }[] = [];
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    calls.push({ url, method: init?.method ?? 'GET' });
    const body = routes[url];
    return new Response(JSON.stringify(body ?? { error: 'not found' }), {
      status: body ? 200 : 404,
      headers: { 'Content-Type': 'application/json' },
    });
  });
  render(
    <ToastProvider>
      <AuthProvider>
        <MemoryRouter initialEntries={['/jobs?job=5']}>
          <AppRoutes />
        </MemoryRouter>
      </AuthProvider>
    </ToastProvider>,
  );
  return calls;
}

describe('Job monitoring page', () => {
  beforeEach(() => vi.restoreAllMocks());
  afterEach(() => vi.useRealTimers());

  it('shows partial batch progress, hold, and only backend-allowed actions', async () => {
    setup({
      '/api/auth/me': me,
      '/api/v2/jobs': { items: [job] },
      '/api/v2/jobs/5': {
        job,
        batches: [{ batch_seq: 3, cursor_from: 200, cursor_to: 300, rows_affected: 100, status: 'committed', committed_at: '2026-11-25T00:00:00Z' }],
        events: [{ id: 1, event: 'job.partial', status: 'partial', message: 'batch 4 failed', created_at: '2026-11-25T00:00:00Z' }],
        actions: { resume: true, cancel: false, compensate: true },
      },
    });
    await waitFor(() => expect(screen.getAllByText('Partial').length).toBeGreaterThan(0));
    expect(await screen.findByText(/Database ini ditahan/)).toBeInTheDocument();
    expect(screen.getByText(/300 \/ 1[.,]?000 row/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Resume \/ Retry/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Batalkan batch committed/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Cancel$/ })).not.toBeInTheDocument();
    expect(screen.getByText('(200, 300]')).toBeInTheDocument();
  });

  it('polls active jobs with GET only and never re-publishes', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const running = { ...job, status: 'running', request_status: 'running', held: false, last_error: '' };
    const calls = setup({
      '/api/auth/me': me,
      '/api/v2/jobs': { items: [running] },
      '/api/v2/jobs/5': { job: running, batches: [], events: [], actions: { resume: false, cancel: true, compensate: false } },
    });
    await waitFor(() => expect(screen.getAllByText('Running').length).toBeGreaterThan(0));
    const before = calls.filter((c) => c.url === '/api/v2/jobs').length;
    await act(async () => {
      vi.advanceTimersByTime(6000);
    });
    await waitFor(() => expect(calls.filter((c) => c.url === '/api/v2/jobs').length).toBeGreaterThan(before));
    expect(calls.every((c) => c.method === 'GET')).toBe(true);
  });

  it('asks for confirmation before cancelling', async () => {
    const queued = { ...job, status: 'queued', request_status: 'pending', mode: 'normal', held: false, last_error: '' };
    const calls = setup({
      '/api/auth/me': me,
      '/api/v2/jobs': { items: [queued] },
      '/api/v2/jobs/5': { job: queued, batches: [], events: [], actions: { resume: false, cancel: true, compensate: false } },
    });
    const btn = await screen.findByRole('button', { name: /Cancel/ });
    fireEvent.click(btn);
    expect(await screen.findByText('Batalkan job?')).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });
});
