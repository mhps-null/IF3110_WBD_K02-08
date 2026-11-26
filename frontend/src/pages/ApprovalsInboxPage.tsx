import { useCallback, useEffect, useState } from 'react';
import { lifecycleApi, type LifecycleRequest, type LifecycleApproval, type LifecycleApprovalDecision } from '../api/lifecycle';
import { ApiError } from '../api/client';
import { useToast } from '../context/ToastContext';
import { PageHeader, EmptyState, ErrorState, LoadingState } from '../components/shared/UIStates';

export function ApprovalsInboxPage() {
  const { notify } = useToast();

  const [items, setItems] = useState<LifecycleRequest[]>([]);
  const [detail, setDetail] = useState<{
    request: LifecycleRequest;
    approvals: LifecycleApproval[];
    approval_history: LifecycleApprovalDecision[];
    sql: string;
  } | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    setLoading(true);
    setError('');

    try {
      const result = await lifecycleApi.inbox();
      setItems(result.items ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Gagal memuat inbox');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const open = async (r: LifecycleRequest) => {
    try {
      const d = await lifecycleApi.detail(r.db_id, r.id);

      setDetail({
        request: d.request,
        approvals: d.approvals,
        approval_history: d.approval_history,
        sql: r.action === 'rollback' ? d.definition.sql_down : d.definition.sql_up,
      });
    } catch (e) {
      notify('error', e instanceof Error ? e.message : 'Detail gagal dimuat');
    }
  };

  const decide = async (decision: 'approve' | 'reject') => {
    if (!detail) return;

    setBusy(true);

    try {
      const { db_id, id, revision_id } = detail.request;
      await lifecycleApi.decide(db_id, id, revision_id, decision);
      notify('success', `Request #${detail.request.id} ${decision}`);
      setDetail(null);
      await refresh();
    } catch (e) {
      notify('error', e instanceof Error ? e.message : 'Gagal memproses approval');
      if (e instanceof ApiError && e.status === 409) {
        setDetail(null);
        await refresh();
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-5">
      <PageHeader
        title="Review Inbox & Approvals"
        description="Setiap leader menyetujui seluruh group yang dipimpinnya dalam satu tindakan."
      />

      {loading && <LoadingState label="Memuat review inbox..." />}
      {error && <ErrorState message={error} onRetry={() => void refresh()} />}

      {!loading && !error && items.length === 0 && (
        <EmptyState
          title="Tidak ada review menunggu keputusanmu"
          description="Migration yang memerlukan persetujuanmu akan muncul di sini."
        />
      )}

      {items.map((r) => (
        <div
          key={r.id}
          className="card p-4 flex flex-wrap items-center justify-between gap-3"
        >
          <div className="text-sm">
            Request #{r.id} · DB #{r.db_id} · Migration #{r.migration_id} · {r.action} · Revision #{r.revision_id}
          </div>
          <button className="btn-secondary" onClick={() => void open(r)}>
            Review
          </button>
        </div>
      ))}

      {detail && (
        <section className="card p-5 space-y-3">
          <div className="flex justify-between">
            <h2 className="font-semibold">
              Review request #{detail.request.id}
            </h2>
            <button className="btn-secondary" onClick={() => setDetail(null)}>
              Tutup
            </button>
          </div>

          <p className="text-sm">
            Tindakan: {detail.request.action}
          </p>

          <pre className="bg-slate-900 text-white rounded p-3 overflow-x-auto text-xs">
            {detail.sql}
          </pre>

          <ul className="text-sm">
            {detail.approvals.map((a) => (
              <li key={a.group_id}>
                Group #{a.group_id} · Leader {a.leader_id ?? 'belum ditetapkan'} ·{' '}
                {a.decision === 'approved' ? 'Approved' : a.decision === 'rejected' ? 'Rejected' : 'Pending'}
              </li>
            ))}
          </ul>

          {detail.approval_history.length > 0 && (
            <ul className="text-sm space-y-1">
              {detail.approval_history.map((decision) => (
                <li key={decision.id}>
                  Revision #{decision.revision_id} · Group #{decision.group_id} · {decision.decision} by user #{decision.actor_id}
                </li>
              ))}
            </ul>
          )}

          <div className="flex gap-2">
            <button
              className="btn-primary"
              disabled={busy}
              onClick={() => void decide('approve')}
            >
              Approve
            </button>
            <button
              className="btn-secondary"
              disabled={busy}
              onClick={() => {
                if (window.confirm('Tolak request ini?')) void decide('reject');
              }}
            >
              Reject
            </button>
          </div>
        </section>
      )}
    </div>
  );
}
