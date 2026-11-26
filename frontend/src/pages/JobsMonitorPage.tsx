import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Activity, Info, RefreshCw, RotateCcw, Square, Undo2 } from 'lucide-react';
import { useAuth } from '../context/AuthContext';
import { useToast } from '../context/ToastContext';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';
import { ConfirmDialog } from '../components/shared/ConfirmDialog';
import { isActive, jobsApi, type JobDetail, type JobStatus, type MigrationJob } from '../api/jobs';

const POLL_MS = 2500;

type Variant = 'neutral' | 'indigo' | 'emerald' | 'amber' | 'rose' | 'sky';

export const statusMeta: Record<JobStatus, { label: string; variant: Variant; hint: string }> = {
  queued: { label: 'Pending (Queue)', variant: 'neutral', hint: 'Menunggu worker mengambil job.' },
  retrying: { label: 'Retrying', variant: 'amber', hint: 'Gagal sementara; dijadwalkan ulang dengan backoff.' },
  running: { label: 'Running', variant: 'sky', hint: 'Worker memegang lock database dan sedang mengeksekusi.' },
  compensating: { label: 'Compensating', variant: 'sky', hint: 'Membatalkan batch committed dengan SQL down (urutan terbalik).' },
  uncertain: { label: 'Uncertain', variant: 'amber', hint: 'Hasil commit belum pasti; worker memverifikasi ledger target sebelum melanjutkan. Database ditahan.' },
  success: { label: 'Success', variant: 'emerald', hint: 'Seluruh perubahan committed dan tercatat di history.' },
  failed: { label: 'Failed', variant: 'rose', hint: 'Transaction di-rollback; attempt gagal tercatat.' },
  partial: { label: 'Partial', variant: 'amber', hint: 'Sebagian batch committed. Database ditahan sampai resume atau kompensasi.' },
  partially_reverted: { label: 'Partially Reverted', variant: 'amber', hint: 'Kompensasi baru sebagian. Lanjutkan kompensasi atau resume.' },
  reverted: { label: 'Reverted', variant: 'indigo', hint: 'Semua batch committed sudah dikompensasi.' },
  cancelled: { label: 'Cancelled', variant: 'neutral', hint: 'Dibatalkan pengguna.' },
  rejected: { label: 'Rejected', variant: 'rose', hint: 'Revalidasi worker menolak eksekusi (approval/izin/urutan berubah). Tidak ada SQL yang dijalankan.' },
};

export function JobStatusBadge({ status }: { status: JobStatus }) {
  const m = statusMeta[status] ?? { label: status, variant: 'neutral' as const };
  return <Badge variant={m.variant}>{m.label}</Badge>;
}

const fmt = (s?: string | null) => (s ? new Date(s).toLocaleString() : '—');
const num = (n?: number | null) => (n === null || n === undefined ? '—' : n.toLocaleString());

function progressOf(j: MigrationJob) {
  if (j.mode !== 'batch' || !j.total_rows) return null;
  const net = j.op === 'compensate' || j.batches_reverted > 0 ? j.rows_done - j.rows_reverted : j.rows_done;
  return Math.max(0, Math.min(100, Math.round((net / j.total_rows) * 100)));
}

function ProgressBar({ value }: { value: number }) {
  return (
    <div className="h-2 w-full overflow-hidden rounded-full bg-slate-200" role="progressbar" aria-valuenow={value} aria-valuemin={0} aria-valuemax={100}>
      <div className="h-full rounded-full bg-indigo-600 transition-all" style={{ width: `${value}%` }} />
    </div>
  );
}

type Pending = { kind: 'resume' | 'cancel' | 'compensate'; job: MigrationJob } | null;

export function JobsMonitorPage() {
  const { accessibleDatabases } = useAuth();
  const { notify } = useToast();
  const [params, setParams] = useSearchParams();
  const selectedId = Number(params.get('job') || 0);
  const [dbFilter, setDbFilter] = useState<number>(Number(params.get('db') || 0));
  const [statusFilter, setStatusFilter] = useState<string>('');
  const [jobs, setJobs] = useState<MigrationJob[]>([]);
  const [detail, setDetail] = useState<JobDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<Pending>(null);
  const inFlight = useRef(false);

  const load = useCallback(
    async (quiet = false) => {
      if (inFlight.current) return;
      inFlight.current = true;
      if (!quiet) setLoading(true);
      try {
        const [list, d] = await Promise.all([
          jobsApi.list({ db_id: dbFilter || undefined, status: statusFilter || undefined }),
          selectedId ? jobsApi.detail(selectedId) : Promise.resolve(null),
        ]);
        setJobs(list.items ?? []);
        setDetail(d);
        setError('');
      } catch (e) {
        if (!quiet) setError(e instanceof Error ? e.message : 'Gagal memuat job');
      } finally {
        inFlight.current = false;
        setLoading(false);
      }
    },
    [dbFilter, statusFilter, selectedId],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const anyActive = useMemo(
    () => jobs.some((j) => isActive(j.status)) || (detail ? isActive(detail.job.status) : false),
    [jobs, detail],
  );

  // Polling only reads status (GET); it never creates or re-publishes a job.
  useEffect(() => {
    if (!anyActive) return;
    const t = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load(true);
    }, POLL_MS);
    return () => window.clearInterval(t);
  }, [anyActive, load]);

  const select = (id: number | null) => {
    const next = new URLSearchParams(params);
    if (id) next.set('job', String(id));
    else next.delete('job');
    setParams(next, { replace: true });
  };

  const runAction = async () => {
    if (!pending) return;
    setBusy(true);
    try {
      const fn = { resume: jobsApi.resume, cancel: jobsApi.cancel, compensate: jobsApi.compensate }[pending.kind];
      await fn(pending.job.id);
      notify('success', 'Permintaan diterima; worker akan memprosesnya secara asynchronous.');
      setPending(null);
      await load(true);
    } catch (e) {
      notify('error', e instanceof Error ? e.message : 'Aksi gagal');
    } finally {
      setBusy(false);
    }
  };

  const dialogText: Record<'resume' | 'cancel' | 'compensate', { title: string; description: string; destructive: boolean }> = {
    resume: {
      title: 'Lanjutkan job?',
      description:
        'Worker melanjutkan dari checkpoint terakhir yang sudah committed tanpa mengulang batch selesai. Approval tetap berlaku untuk entry yang sama, namun worker merevalidasi approval & izin sebelum eksekusi.',
      destructive: false,
    },
    cancel: {
      title: 'Batalkan job?',
      description:
        'Job di antrean dikeluarkan dari queue. Job batch yang berjalan berhenti setelah batch saat ini commit; batch yang sudah committed TIDAK dihapus.',
      destructive: true,
    },
    compensate: {
      title: 'Batalkan batch committed?',
      description:
        'Worker menjalankan SQL down yang immutable dan telah disetujui per batch dalam urutan terbalik. Jika gagal di tengah, status menjadi Partially Reverted dan dapat dilanjutkan.',
      destructive: true,
    },
  };

  return (
    <div className="min-w-0">
      <PageHeader
        title="Job Monitoring & Batch Progress"
        description="Status eksekusi asynchronous worker: queue, lock, retry, progress batch/cursor, checkpoint, resume, dan kompensasi."
      />

      <div className="mb-4 rounded-xl border border-indigo-200 bg-indigo-50/70 p-3 text-xs text-indigo-900">
        <div className="flex items-start gap-2">
          <Info className="mt-0.5 h-4 w-4 shrink-0 text-indigo-600" />
          <p>
            Enqueue yang berhasil <strong>bukan</strong> keberhasilan migration. Status <em>Success</em> hanya muncul
            setelah worker commit dan mencatat attempt pada Migration History. Halaman ini hanya membaca status (polling
            tiap {POLL_MS / 1000} detik saat ada job aktif).
          </p>
        </div>
      </div>

      <div className="mb-4 flex flex-col gap-2 sm:flex-row sm:items-end">
        <label className="flex-1 text-xs font-medium text-slate-600">
          Database
          <select className="input-field mt-1 w-full" value={dbFilter} onChange={(e) => setDbFilter(Number(e.target.value))}>
            <option value={0}>Semua database yang dapat diakses</option>
            {accessibleDatabases.map((d) => (
              <option key={d.db_id} value={d.db_id}>
                {d.name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex-1 text-xs font-medium text-slate-600">
          Status
          <select className="input-field mt-1 w-full" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
            <option value="">Semua status</option>
            {(Object.keys(statusMeta) as JobStatus[]).map((s) => (
              <option key={s} value={s}>
                {statusMeta[s].label}
              </option>
            ))}
          </select>
        </label>
        <button type="button" className="btn-secondary text-xs sm:mb-0.5" onClick={() => void load()}>
          <RefreshCw className="h-3.5 w-3.5" /> Muat ulang
        </button>
      </div>

      {loading && !jobs.length ? (
        <LoadingState label="Memuat job..." />
      ) : error ? (
        <ErrorState message={error} onRetry={() => void load()} />
      ) : (
        <div className="grid gap-4 lg:grid-cols-5">
          <section className="space-y-2 lg:col-span-2" aria-label="Daftar job">
            {jobs.length === 0 ? (
              <EmptyState
                title="Belum ada job"
                description="Job muncul setelah migration yang sudah Approved dieksekusi dari halaman Migration."
                action={
                  <Link to="/migrations" className="btn-secondary text-xs">
                    <Activity className="h-4 w-4" /> Buka Migration
                  </Link>
                }
              />
            ) : (
              jobs.map((j) => {
                const p = progressOf(j);
                return (
                  <button
                    key={j.id}
                    type="button"
                    onClick={() => select(j.id)}
                    className={`card w-full p-3 text-left transition hover:border-indigo-300 ${selectedId === j.id ? 'ring-2 ring-indigo-500' : ''}`}
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="min-w-0 truncate text-sm font-semibold text-slate-900">
                        Job #{j.id} · {j.title || `Migration ${j.migration_id}`}
                      </span>
                      <JobStatusBadge status={j.status} />
                    </div>
                    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-slate-500">
                      <span>{j.db_name || `db ${j.db_id}`}</span>
                      <span className="uppercase">{j.action}</span>
                      <span>{j.mode === 'batch' ? `batch ${num(j.batch_size)}` : 'normal'}</span>
                      {j.op === 'compensate' && <span>kompensasi</span>}
                      <span>attempt {j.attempts}/{j.max_attempts}</span>
                      {j.held && <span className="font-medium text-amber-700">DB ditahan</span>}
                    </div>
                    {p !== null && (
                      <div className="mt-2">
                        <ProgressBar value={p} />
                      </div>
                    )}
                  </button>
                );
              })
            )}
          </section>

          <section className="min-w-0 lg:col-span-3" aria-label="Detail job">
            {detail ? (
              <JobDetailPanel detail={detail} busy={busy} onClose={() => select(null)} onAction={(kind) => setPending({ kind, job: detail.job })} />
            ) : (
              <div className="card p-6 text-sm text-slate-500">Pilih job untuk melihat progress, checkpoint batch, dan timeline worker.</div>
            )}
          </section>
        </div>
      )}

      <ConfirmDialog
        open={pending !== null}
        title={pending ? dialogText[pending.kind].title : ''}
        description={pending ? dialogText[pending.kind].description : ''}
        destructive={pending ? dialogText[pending.kind].destructive : false}
        loading={busy}
        onConfirm={() => void runAction()}
        onCancel={() => setPending(null)}
      />
    </div>
  );
}

function JobDetailPanel({
  detail,
  busy,
  onClose,
  onAction,
}: {
  detail: JobDetail;
  busy: boolean;
  onClose: () => void;
  onAction: (kind: 'resume' | 'cancel' | 'compensate') => void;
}) {
  const { job: j, batches, events, actions } = detail;
  const p = progressOf(j);
  const meta = statusMeta[j.status];
  return (
    <div className="card space-y-4 p-4 sm:p-5">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="truncate text-base font-semibold text-slate-900">
            Job #{j.id} · {j.title || `Migration ${j.migration_id}`}
          </h2>
          <p className="mt-0.5 text-xs text-slate-500">
            Request #{j.request_id} ({j.request_status}) · revisi {j.revision_id} ·{' '}
            <Link className="underline" to={`/databases/${j.db_id}/migrations`}>
              {j.db_name || `db ${j.db_id}`}
            </Link>
          </p>
        </div>
        <div className="flex items-center gap-2">
          <JobStatusBadge status={j.status} />
          <button type="button" className="btn-secondary text-xs" onClick={onClose}>
            Tutup
          </button>
        </div>
      </div>

      {meta && <p className="rounded-lg bg-slate-50 p-2 text-xs text-slate-600">{meta.hint}</p>}

      {j.mode === 'batch' && (
        <div className="space-y-1.5">
          <div className="flex justify-between text-xs text-slate-600">
            <span>
              {num(j.rows_done - j.rows_reverted)} / {num(j.total_rows)} row · {j.batches_done - j.batches_reverted} batch aktif
            </span>
            <span>{p !== null ? `${p}%` : ''}</span>
          </div>
          {p !== null && <ProgressBar value={p} />}
        </div>
      )}

      <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs sm:grid-cols-3">
        <Item k="Aksi" v={j.action.toUpperCase()} />
        <Item k="Mode" v={j.mode === 'batch' ? `Batch (${num(j.batch_size)})` : 'Normal (1 transaction)'} />
        <Item k="Operasi" v={j.op === 'compensate' ? 'Kompensasi' : 'Apply'} />
        {j.mode === 'batch' && <Item k="Cursor" v={j.batch_table ? `${j.batch_table}.${j.batch_column}` : j.source_job_id ? `rentang job #${j.source_job_id}` : '—'} />}
        {j.mode === 'batch' && <Item k="Cakupan" v={j.scope_from !== null && j.scope_from !== undefined ? `(${num(j.scope_from)}, ${num(j.scope_to)}]` : '—'} />}
        {j.mode === 'batch' && <Item k="Checkpoint" v={num(j.last_cursor)} />}
        <Item k="Attempt" v={`${j.attempts} / ${j.max_attempts}`} />
        <Item k="Retry otomatis" v={String(j.retry_count)} />
        <Item k="Delivery / recovery" v={`${j.deliveries} / ${j.recoveries}`} />
        <Item k="Rows reverted" v={num(j.rows_reverted)} />
        <Item k="Worker" v={j.lease_owner || '—'} />
        <Item k="Heartbeat" v={fmt(j.heartbeat_at)} />
        <Item k="Enqueue" v={fmt(j.queued_at)} />
        <Item k="Mulai" v={fmt(j.started_at)} />
        <Item k="Selesai" v={fmt(j.finished_at)} />
        {(j.status === 'retrying' || j.status === 'uncertain') && <Item k="Jadwal berikut" v={fmt(j.next_run_at)} />}
        {j.correlation_id && <Item k="Correlation / trace" v={j.correlation_id} mono />}
      </dl>

      {j.held && (
        <p className="rounded-lg border border-amber-200 bg-amber-50 p-2 text-xs text-amber-900">
          Database ini ditahan: migration lain pada database yang sama tidak dapat diajukan/dieksekusi sampai job ini
          selesai di-resume, dikompensasi, atau hasil commit diverifikasi.
        </p>
      )}
      {j.cancel_requested && <p className="text-xs font-medium text-amber-700">Pembatalan diminta; worker berhenti setelah batch saat ini.</p>}
      {j.last_error && (
        <p className="break-words rounded-lg border border-rose-200 bg-rose-50 p-2 font-mono text-xs text-rose-800">{j.last_error}</p>
      )}

      {(actions.resume || actions.cancel || actions.compensate) && (
        <div className="flex flex-wrap gap-2">
          {actions.resume && (
            <button type="button" disabled={busy} className="btn-primary text-xs" onClick={() => onAction('resume')}>
              <RotateCcw className="h-3.5 w-3.5" /> {j.status === 'uncertain' ? 'Verifikasi sekarang' : 'Resume / Retry'}
            </button>
          )}
          {actions.compensate && (
            <button type="button" disabled={busy} className="btn-secondary text-xs" onClick={() => onAction('compensate')}>
              <Undo2 className="h-3.5 w-3.5" /> Batalkan batch committed
            </button>
          )}
          {actions.cancel && (
            <button type="button" disabled={busy} className="btn-secondary text-xs text-rose-700" onClick={() => onAction('cancel')}>
              <Square className="h-3.5 w-3.5" /> Cancel
            </button>
          )}
        </div>
      )}

      {batches.length > 0 && (
        <div>
          <h3 className="mb-1.5 text-sm font-semibold text-slate-800">Batch committed ({j.batches_done})</h3>
          <div className="max-h-64 overflow-auto rounded-lg border border-slate-200">
            <table className="w-full min-w-[480px] text-left text-xs">
              <thead className="sticky top-0 bg-slate-50 text-slate-600">
                <tr>
                  <th className="px-2 py-1.5">#</th>
                  <th className="px-2 py-1.5">Cursor (from, to]</th>
                  <th className="px-2 py-1.5">Rows</th>
                  <th className="px-2 py-1.5">Status</th>
                  <th className="px-2 py-1.5">Commit</th>
                </tr>
              </thead>
              <tbody>
                {batches.map((b) => (
                  <tr key={b.batch_seq} className="border-t border-slate-100">
                    <td className="px-2 py-1">{b.batch_seq}</td>
                    <td className="px-2 py-1 font-mono">
                      ({b.cursor_from}, {b.cursor_to}]
                    </td>
                    <td className="px-2 py-1">{b.rows_affected.toLocaleString()}</td>
                    <td className="px-2 py-1">
                      <Badge variant={b.status === 'reverted' ? 'indigo' : 'emerald'}>{b.status}</Badge>
                    </td>
                    <td className="px-2 py-1 whitespace-nowrap">{fmt(b.committed_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <div>
        <h3 className="mb-1.5 text-sm font-semibold text-slate-800">Timeline worker</h3>
        <ol className="max-h-72 space-y-1.5 overflow-auto pr-1">
          {events.map((e) => (
            <li key={e.id} className="rounded-lg border border-slate-100 p-2 text-xs">
              <div className="flex flex-wrap justify-between gap-1">
                <span className="font-mono font-medium text-slate-800">{e.event}</span>
                <span className="text-slate-400">{fmt(e.created_at)}</span>
              </div>
              {e.message && <p className="mt-0.5 break-words text-slate-600">{e.message}</p>}
              <p className="mt-0.5 text-slate-400">
                {e.worker_id && `worker ${e.worker_id}`}
                {e.attempt ? ` · attempt ${e.attempt}` : ''}
                {e.batch_seq ? ` · batch ${e.batch_seq}` : ''}
              </p>
            </li>
          ))}
        </ol>
      </div>
    </div>
  );
}

function Item({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-slate-400">{k}</dt>
      <dd className={`truncate font-medium text-slate-800 ${mono ? 'font-mono' : ''}`} title={v}>
        {v}
      </dd>
    </div>
  );
}
