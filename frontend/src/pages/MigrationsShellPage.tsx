import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useAuth } from '../context/AuthContext';
import { useToast } from '../context/ToastContext';
import { lifecycleApi, type LifecycleDefinition, type LifecycleExecution, type LifecycleRequest, type LifecycleApproval, type LifecycleApprovalDecision } from '../api/lifecycle';
import { PageHeader, LoadingState, ErrorState, EmptyState, Badge } from '../components/shared/UIStates';
import { ExecuteDialog } from '../components/jobs/ExecuteDialog';

const blank = {
  title: '',
  sql_up: '',
  sql_down: '',
  irreversible_reason: '',
};

type Details = {
  request: LifecycleRequest;
  definition: LifecycleDefinition;
  approvals: LifecycleApproval[];
  approval_history: LifecycleApprovalDecision[];
};

export function MigrationsShellPage() {
  const { id } = useParams<{ id: string }>();
  const { accessibleDatabases, user, refreshSession } = useAuth();
  const { notify } = useToast();
  const navigate = useNavigate();

  useEffect(() => {
    void refreshSession();
  }, [refreshSession]);

  const db = id ? Number(id) : accessibleDatabases[0]?.db_id ?? 0;
  const current = accessibleDatabases.find((x) => x.db_id === db);
  const canEdit = current?.level === 2 || current?.level_label === 'View + Edit';

  const [requests, setRequests] = useState<LifecycleRequest[]>([]);
  const [history, setHistory] = useState<LifecycleExecution[]>([]);
  const [hideRollback, setHideRollback] = useState(false);
  const [form, setForm] = useState(blank);
  const [irreversible, setIrreversible] = useState(false);
  const [selected, setSelected] = useState<Details | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<number | null>(null);
  const [executing, setExecuting] = useState<LifecycleRequest | null>(null);

  const load = useCallback(async () => {
    if (!db) return;

    setLoading(true);
    setError('');

    try {
      const [r, h] = await Promise.all([
        lifecycleApi.list(db),
        lifecycleApi.history(db, hideRollback),
      ]);

      setRequests(r.items ?? []);
      setHistory(h.items ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Gagal memuat migration');
    } finally {
      setLoading(false);
    }
  }, [db, hideRollback]);

  useEffect(() => {
    void load();
    setSelected(null);
  }, [load]);

  const act = async (description: string, fn: () => Promise<unknown>) => {
    setBusy(true);

    try {
      await fn();
      notify('success', `${description} berhasil`);
      await load();
      setSelected(null);
      setEditing(null);
    } catch (e) {
      notify('error', e instanceof Error ? e.message : 'Operasi gagal');
    } finally {
      setBusy(false);
    }
  };

  const definition = () => ({
    title: form.title,
    sql_up: form.sql_up,
    sql_down: irreversible ? '' : form.sql_down,
    irreversible_reason: irreversible ? form.irreversible_reason : '',
  });

  const open = async (r: LifecycleRequest) => {
    try {
      setSelected(await lifecycleApi.detail(db, r.id));
    } catch (e) {
      notify('error', e instanceof Error ? e.message : 'Gagal membuka detail');
    }
  };

  const edit = (d: Details) => {
    setEditing(d.request.id);

    setForm({
      title: d.definition.title,
      sql_up: d.definition.sql_up,
      sql_down: d.definition.sql_down,
      irreversible_reason: d.definition.irreversible_reason,
    });

    setIrreversible(Boolean(d.definition.irreversible_reason));
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Migration Lifecycle & History"
        description="SQL Up/Down, review seluruh group, eksekusi asynchronous, rollback/forward, dan riwayat immutable."
        actions={
          <select
            aria-label="Database target"
            className="input-field min-w-48"
            value={db || ''}
            onChange={(e) => navigate(`/databases/${e.target.value}/migrations`)}
          >
            {accessibleDatabases.map((d) => (
              <option key={d.db_id} value={d.db_id}>
                {d.name}
              </option>
            ))}
          </select>
        }
      />

      {!db ? (
        <EmptyState
          title="Tidak ada database"
          description="Akunmu belum memiliki akses database."
        />
      ) : (
        <>
          {canEdit && (
            <section className="card p-5 space-y-3">
              <h2 className="font-semibold text-sm">
                {editing ? `Revisi draft request #${editing}` : 'Buat migration baru'}
              </h2>

              <input
                className="input-field w-full"
                placeholder="Judul migration"
                aria-label="Judul"
                value={form.title}
                onChange={(e) => setForm({ ...form, title: e.target.value })}
              />

              <label className="block text-sm">
                SQL Up
                <textarea
                  className="input-field w-full font-mono mt-1"
                  rows={5}
                  value={form.sql_up}
                  onChange={(e) => setForm({ ...form, sql_up: e.target.value })}
                />
              </label>

              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={irreversible}
                  onChange={(e) => setIrreversible(e.target.checked)}
                />
                Irreversible (tidak bisa rollback)
              </label>

              {irreversible ? (
                <label className="block text-sm">
                  Alasan irreversible
                  <textarea
                    className="input-field w-full mt-1"
                    value={form.irreversible_reason}
                    onChange={(e) =>
                      setForm({ ...form, irreversible_reason: e.target.value })
                    }
                  />
                </label>
              ) : (
                <label className="block text-sm">
                  SQL Down
                  <textarea
                    className="input-field w-full font-mono mt-1"
                    rows={5}
                    value={form.sql_down}
                    onChange={(e) => setForm({ ...form, sql_down: e.target.value })}
                  />
                </label>
              )}

              <div className="flex gap-2 flex-wrap">
                <button
                  disabled={busy || !form.title.trim() || !form.sql_up.trim()}
                  className="btn-primary"
                  onClick={() =>
                    void act(
                      editing ? 'Revisi' : 'Buat draft',
                      () =>
                        editing
                          ? lifecycleApi.revise(db, editing, definition())
                          : lifecycleApi.create(db, definition())
                    )
                  }
                >
                  {editing ? 'Simpan revisi' : 'Simpan draft'}
                </button>

                {editing && (
                  <button
                    className="btn-secondary"
                    onClick={() => {
                      setEditing(null);
                      setForm(blank);
                    }}
                  >
                    Batal revisi
                  </button>
                )}
              </div>

              <p className="text-xs text-slate-500">
                Perubahan SQL setelah attempt tidak diperbolehkan. Untuk mengoreksi migration yang
                pernah dicoba, buat migration baru.
              </p>
            </section>
          )}

          <section className="card p-5 space-y-3">
            <h2 className="font-semibold text-sm">
              Permintaan migration
            </h2>

            {loading && <LoadingState label="Memuat..." />}
            {error && <ErrorState message={error} onRetry={() => void load()} />}

            {!loading && !error && requests.length === 0 && (
              <EmptyState
                title="Belum ada migration M2"
                description="Buat draft untuk memulai review."
              />
            )}

            {requests.map((r) => (
              <div
                className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-100 py-3"
                key={r.id}
              >
                <div className="text-sm">
                  <strong>#{r.id}</strong> · Migration #{r.migration_id} · {r.action} ·{' '}
                  <Badge
                    variant={
                      r.status === 'approved'
                        ? 'emerald'
                        : r.status === 'failed'
                          ? 'rose'
                          : 'neutral'
                    }
                  >
                    {r.status}
                  </Badge>
                </div>

                <div className="flex gap-2 flex-wrap">
                  <button
                    className="btn-secondary text-xs"
                    onClick={() => void open(r)}
                  >
                    Detail / Review
                  </button>

                  {canEdit && user?.id === r.requested_by && r.status === 'draft' && (
                    <>
                      <button
                        disabled={busy}
                        className="btn-secondary text-xs"
                        onClick={() =>
                          void act('Validasi', () => lifecycleApi.validate(db, r.id))
                        }
                      >
                        Validasi SQL
                      </button>

                      <button
                        disabled={busy}
                        className="btn-primary text-xs"
                        onClick={() =>
                          void act('Submit', () => lifecycleApi.submit(db, r.id))
                        }
                      >
                        Submit review
                      </button>
                    </>
                  )}

                  {canEdit && user?.id === r.requested_by && ['pending_review', 'approved'].includes(r.status) && (
                    <button
                      disabled={busy}
                      className="btn-secondary text-xs"
                      onClick={() =>
                        void act('Perbarui reviewer', () => lifecycleApi.refreshReview(db, r.id))
                      }
                    >
                      Refresh review
                    </button>
                  )}

                  {canEdit && r.status === 'approved' && (
                    <button
                      disabled={busy}
                      className="btn-primary text-xs"
                      onClick={() => setExecuting(r)}
                    >
                      Execute
                    </button>
                  )}
                </div>
              </div>
            ))}
          </section>

          {selected && (
            <section className="card p-5 space-y-3">
              <div className="flex justify-between gap-2">
                <h2 className="font-semibold">
                  Detail request #{selected.request.id}
                </h2>
                <button
                  className="btn-secondary text-xs"
                  onClick={() => setSelected(null)}
                >
                  Tutup
                </button>
              </div>

              <p className="text-sm">
                Tindakan: {selected.request.action}, status: {selected.request.status}, revisi #{selected.request.revision_id}
              </p>

              <h3 className="text-sm font-semibold">SQL Up</h3>
              <pre className="overflow-x-auto bg-slate-900 text-white p-3 rounded text-xs">
                {selected.definition.sql_up}
              </pre>

              <h3 className="text-sm font-semibold">SQL Down / irreversible</h3>
              <pre className="overflow-x-auto bg-slate-900 text-white p-3 rounded text-xs">
                {selected.definition.sql_down || selected.definition.irreversible_reason}
              </pre>

              <h3 className="text-sm font-semibold">
                Status approval setiap group
              </h3>

              <ul className="text-sm space-y-1">
                {selected.approvals.map((a) => (
                  <li key={a.group_id}>
                    Group #{a.group_id}: {a.leader_id ? `Leader #${a.leader_id}` : 'Belum punya leader'} —{' '}
                    {a.decision === 'approved'
                      ? `Approved by #${a.decided_by ?? a.approved_by}`
                      : a.decision === 'rejected'
                        ? `Rejected by #${a.decided_by}`
                        : 'Pending'}
                  </li>
                ))}
              </ul>

              {selected.approval_history.length > 0 && (
                <div>
                  <h3 className="text-sm font-semibold">Riwayat keputusan</h3>
                  <ul className="text-sm space-y-1">
                    {selected.approval_history.map((decision) => (
                      <li key={decision.id}>
                        Revision #{decision.revision_id} · Group #{decision.group_id} ·{' '}
                        {decision.decision === 'approved' ? 'Approved' : 'Rejected'} by user #{decision.actor_id} ·{' '}
                        {new Date(decision.decided_at).toLocaleString('id-ID')}
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              {canEdit &&
                user?.id === selected.request.requested_by &&
                ['draft', 'pending_review', 'approved', 'rejected'].includes(selected.request.status) &&
                selected.request.action === 'up' && (
                  <button
                    className="btn-secondary"
                    onClick={() => edit(selected)}
                  >
                    Buat revisi
                  </button>
                )}
            </section>
          )}

          <section className="card p-5 space-y-3">
            <div className="flex justify-between flex-wrap gap-2">
              <h2 className="font-semibold text-sm">
                Execution History
              </h2>

              <label className="text-sm flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={hideRollback}
                  onChange={(e) => setHideRollback(e.target.checked)}
                />
                Hide rollback (berdasarkan pasangan execution ID)
              </label>
            </div>

            {history.length === 0 && (
              <p className="text-sm text-slate-500">
                Belum ada execution record.
              </p>
            )}

            {history.map((e) => (
              <div
                key={e.attempt_id}
                className="border-b border-slate-100 py-3 text-sm space-y-2"
              >
                <div className="flex flex-wrap justify-between gap-2">
                  <strong>
                    Execution #{e.execution_id} · attempt #{e.attempt} · {e.action} · {e.status}
                  </strong>
                  <span>
                    {new Date(e.executed_at).toLocaleString('id-ID')}
                  </span>
                </div>

                <p>
                  Migration #{e.migration_id}, revisi #{e.revision_id}, user #{e.user_id}, attempt {e.attempt}{' '}
                  {e.rollback_of_execution_id
                    ? `· rollback/forward reference #${e.rollback_of_execution_id}`
                    : ''}
                </p>

                <pre className="overflow-x-auto bg-slate-900 text-white p-3 rounded text-xs">
                  {e.sql_snapshot}
                </pre>

                {e.error_message && (
                  <p role="alert" className="text-rose-700">
                    {e.error_message}
                  </p>
                )}

                {canEdit && e.available_action === 'rollback' && (
                  <button
                    disabled={busy}
                    className="btn-secondary text-xs"
                    onClick={() =>
                      void act('Ajukan rollback', () =>
                        lifecycleApi.action(db, e.migration_id, e.execution_id, 'rollback')
                      )
                    }
                  >
                    Ajukan rollback
                  </button>
                )}

                {canEdit && e.available_action === 'forward' && (
                  <button
                    disabled={busy}
                    className="btn-secondary text-xs"
                    onClick={() =>
                      void act('Ajukan forward', () =>
                        lifecycleApi.action(db, e.migration_id, e.execution_id, 'forward')
                      )
                    }
                  >
                    Ajukan forward
                  </button>
                )}
              </div>
            ))}
          </section>

          <p className="text-xs text-slate-500">
            Setelah enqueue, status job dipantau pada{' '}
            <Link to="/jobs" className="underline">
              Job Monitoring
            </Link>
            . Keberhasilan enqueue bukan keberhasilan eksekusi.
          </p>
        </>
      )}

      {executing && (
        <ExecuteDialog
          open
          db={db}
          requestId={executing.id}
          revisionId={executing.revision_id}
          action={executing.action}
          onClose={() => setExecuting(null)}
          onError={(m) => notify('error', m)}
          onQueued={(jobId) => {
            setExecuting(null);
            notify('success', `Job #${jobId} dipublikasikan ke queue. Pantau progres di Job Monitoring.`);
            navigate(`/jobs?job=${jobId}`);
          }}
        />
      )}
    </div>
  );
}
