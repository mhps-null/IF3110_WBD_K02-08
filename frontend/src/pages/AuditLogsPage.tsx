import React, { useCallback, useEffect, useState } from 'react';
import { ChevronLeft, ChevronRight, Filter, RefreshCw } from 'lucide-react';
import { auditApi } from '../api/endpoints';
import { ApiError } from '../api/client';
import type { AuditPage } from '../types/api';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';

export function AuditLogsPage() {
  const [data, setData] = useState<AuditPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [actor, setActor] = useState('');
  const [event, setEvent] = useState('');
  const [database, setDatabase] = useState('');
  const [status, setStatus] = useState('');
  const [page, setPage] = useState(1);
  const pageSize = 20;

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const res = await auditApi.list({
        actor: actor.trim() || undefined,
        event: event.trim() || undefined,
        database: database.trim() || undefined,
        status: status.trim() || undefined,
        page,
        pageSize,
      });
      setData(res);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Gagal memuat audit log.');
    } finally {
      setLoading(false);
    }
  }, [actor, event, database, status, page]);

  useEffect(() => {
    void load();
  }, [load]);

  const handleFilterSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setPage(1);
    void load();
  };

  const totalPages = data ? Math.max(1, Math.ceil(data.total / pageSize)) : 1;

  return (
    <div>
      <PageHeader
        title="Audit Logs"
        description="Catatan aktivitas penting platform (terintegrasi dengan endpoint GET /api/audit-logs dari Orang 2)."
        actions={
          <button type="button" onClick={() => void load()} className="btn-secondary text-xs">
            <RefreshCw className="h-3.5 w-3.5" />
            Muat Ulang
          </button>
        }
      />

      <form onSubmit={handleFilterSubmit} className="card mb-6 grid grid-cols-1 gap-3 p-4 sm:grid-cols-2 lg:grid-cols-5">
        <input
          type="text"
          value={actor}
          onChange={(e) => setActor(e.target.value)}
          placeholder="Filter actor..."
          className="input-field"
        />
        <input
          type="text"
          value={event}
          onChange={(e) => setEvent(e.target.value)}
          placeholder="Filter event (mis. auth.login)"
          className="input-field"
        />
        <input
          type="text"
          value={database}
          onChange={(e) => setDatabase(e.target.value)}
          placeholder="Filter database..."
          className="input-field"
        />
        <select value={status} onChange={(e) => setStatus(e.target.value)} className="input-field">
          <option value="">Semua status</option>
          <option value="success">success</option>
          <option value="failed">failed</option>
          <option value="rejected">rejected</option>
        </select>
        <button type="submit" className="btn-primary text-xs">
          <Filter className="h-3.5 w-3.5" />
          Terapkan Filter
        </button>
      </form>

      {loading && <LoadingState label="Memuat catatan audit..." />}
      {!loading && error && <ErrorState message={error} onRetry={() => void load()} />}
      {!loading && !error && data && data.items.length === 0 && (
        <EmptyState
          title="Belum ada entri audit"
          description="Belum ada aktivitas yang cocok dengan filter yang dipilih."
        />
      )}

      {!loading && !error && data && data.items.length > 0 && (
        <div className="card overflow-hidden">
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-slate-200 text-left text-xs">
              <thead className="bg-slate-50 font-semibold uppercase text-slate-500">
                <tr>
                  <th className="px-4 py-3">Waktu</th>
                  <th className="px-4 py-3">Actor</th>
                  <th className="px-4 py-3">Event</th>
                  <th className="px-4 py-3">Database</th>
                  <th className="px-4 py-3">Status</th>
                  <th className="px-4 py-3">Trace / Details</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 bg-white">
                {data.items.map((item) => (
                  <tr key={item.id} className="hover:bg-slate-50/70">
                    <td className="whitespace-nowrap px-4 py-3 text-slate-600">
                      {new Date(item.created_at).toLocaleString('id-ID')}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-medium text-slate-900">
                      {item.actor}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-indigo-700">
                      {item.event}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-slate-700">
                      {item.database || '—'}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3">
                      <Badge
                        variant={
                          item.status === 'success'
                            ? 'emerald'
                            : item.status === 'failed' || item.status === 'error'
                              ? 'rose'
                              : 'amber'
                        }
                      >
                        {item.status}
                      </Badge>
                    </td>
                    <td className="max-w-sm px-4 py-3 font-mono text-[11px] text-slate-600">
                      {item.trace_id && (
                        <div className="truncate text-slate-500">trace: {item.trace_id}</div>
                      )}
                      {item.details && Object.keys(item.details).length > 0 && (
                        <div className="truncate" title={JSON.stringify(item.details)}>
                          {JSON.stringify(item.details)}
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="flex items-center justify-between border-t border-slate-200 px-4 py-3 text-xs text-slate-600">
            <span>
              Total <strong>{data.total}</strong> entri · Hal. {data.page} / {totalPages}
            </span>
            <div className="flex gap-2">
              <button
                type="button"
                disabled={page <= 1}
                onClick={() => setPage((p) => Math.max(1, p - 1))}
                className="btn-secondary px-2.5 py-1.5 text-xs"
              >
                <ChevronLeft className="h-3.5 w-3.5" />
                Prev
              </button>
              <button
                type="button"
                disabled={page >= totalPages}
                onClick={() => setPage((p) => p + 1)}
                className="btn-secondary px-2.5 py-1.5 text-xs"
              >
                Next
                <ChevronRight className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
