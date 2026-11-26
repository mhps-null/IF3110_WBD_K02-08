import { useState } from 'react';
import { Play, X } from 'lucide-react';
import { jobsApi, type ExecuteOptions } from '../../api/jobs';

interface Props {
  open: boolean;
  db: number;
  requestId: number;
  revisionId: number;
  action: 'up' | 'rollback' | 'forward';
  onClose: () => void;
  onQueued: (jobId: number) => void;
  onError: (message: string) => void;
}

const IDENT = /^[A-Za-z_][A-Za-z0-9_]{0,62}(\.[A-Za-z_][A-Za-z0-9_]{0,62})?$/;

/**
 * Publishes an approved request as an asynchronous job. The API returns a job ID
 * immediately; success is only reported later by the worker on the Jobs page.
 */
export function ExecuteDialog({ open, db, requestId, revisionId, action, onClose, onQueued, onError }: Props) {
  const [mode, setMode] = useState<'default' | 'normal' | 'batch'>(action === 'up' ? 'normal' : 'default');
  const [table, setTable] = useState('');
  const [column, setColumn] = useState('id');
  const [size, setSize] = useState(10000);
  const [attempts, setAttempts] = useState(3);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  if (!open) return null;

  const submit = async () => {
    setError('');
    const opts: ExecuteOptions = { revision_id: revisionId, max_attempts: attempts };
    if (action !== 'rollback') {
      if (mode === 'batch') {
        if (!IDENT.test(table.trim()) || !/^[A-Za-z_][A-Za-z0-9_]{0,62}$/.test(column.trim())) {
          setError('Nama tabel/kolom tidak valid (huruf, angka, underscore; tabel boleh schema.tabel).');
          return;
        }
        if (!Number.isInteger(size) || size < 1 || size > 1000000) {
          setError('Batch size harus 1–1.000.000.');
          return;
        }
        Object.assign(opts, { mode: 'batch', batch_table: table.trim(), batch_column: column.trim(), batch_size: size });
      } else if (mode === 'normal') {
        opts.mode = 'normal';
      }
    }
    setBusy(true);
    try {
      const res = await jobsApi.execute(db, requestId, opts);
      onQueued(res.job_id);
    } catch (e) {
      const msg = e instanceof Error ? e.message : 'Gagal mempublikasikan job';
      setError(msg);
      onError(msg);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div role="dialog" aria-modal="true" aria-labelledby="execute-title" className="fixed inset-0 z-50 flex items-end justify-center bg-slate-900/50 p-0 sm:items-center sm:p-4">
      <div className="card max-h-[92vh] w-full max-w-lg overflow-y-auto rounded-b-none p-5 sm:rounded-b-xl">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 id="execute-title" className="text-base font-semibold text-slate-900">
              Execute request #{requestId} ({action.toUpperCase()})
            </h2>
            <p className="mt-1 text-xs text-slate-500">
              Job dipublikasikan ke queue dan dikerjakan worker secara asynchronous. Worker mengambil lock database,
              merevalidasi approval & izin, lalu menjalankan SQL immutable yang disetujui.
            </p>
          </div>
          <button type="button" aria-label="Tutup" onClick={onClose} className="rounded-lg p-1 text-slate-400 hover:bg-slate-100">
            <X className="h-4 w-4" />
          </button>
        </div>

        {action === 'rollback' ? (
          <p className="mt-4 rounded-lg bg-slate-50 p-3 text-xs text-slate-600">
            Mode rollback ditentukan otomatis: jika eksekusi yang dibatalkan berjalan per batch, SQL down dijalankan per
            batch atas rentang yang sudah committed dalam urutan terbalik; jika tidak, dalam satu transaction.
          </p>
        ) : (
          <fieldset className="mt-4 space-y-2">
            <legend className="text-xs font-semibold text-slate-700">Mode eksekusi</legend>
            {action === 'forward' && (
              <label className="flex items-start gap-2 text-sm">
                <input type="radio" name="mode" checked={mode === 'default'} onChange={() => setMode('default')} className="mt-1" />
                <span>Ikuti rencana eksekusi sebelumnya (batch bila migration ini dulu dijalankan per batch)</span>
              </label>
            )}
            <label className="flex items-start gap-2 text-sm">
              <input type="radio" name="mode" checked={mode === 'normal'} onChange={() => setMode('normal')} className="mt-1" />
              <span>Normal — seluruh SQL dalam satu transaction</span>
            </label>
            <label className="flex items-start gap-2 text-sm">
              <input type="radio" name="mode" checked={mode === 'batch'} onChange={() => setMode('batch')} className="mt-1" />
              <span>Per batch — tiap batch transaction tersendiri dengan checkpoint</span>
            </label>
          </fieldset>
        )}

        {action !== 'rollback' && mode === 'batch' && (
          <div className="mt-3 space-y-3 rounded-lg border border-slate-200 p-3">
            <p className="text-xs text-slate-600">
              SQL harus memakai <code className="font-mono">$1</code> (cursor bawah, eksklusif) dan{' '}
              <code className="font-mono">$2</code> (cursor atas, inklusif), mis.{' '}
              <code className="break-all font-mono">UPDATE t SET ... WHERE id &gt; $1 AND id &lt;= $2</code>. Kolom cursor harus
              integer, unik, dan tidak berubah.
            </p>
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="text-xs font-medium text-slate-600">
                Tabel (cakupan)
                <input className="input-field mt-1 w-full" value={table} onChange={(e) => setTable(e.target.value)} placeholder="public.large_migration_records" />
              </label>
              <label className="text-xs font-medium text-slate-600">
                Kolom cursor
                <input className="input-field mt-1 w-full" value={column} onChange={(e) => setColumn(e.target.value)} />
              </label>
              <label className="text-xs font-medium text-slate-600">
                Batch size
                <input type="number" min={1} max={1000000} className="input-field mt-1 w-full" value={size} onChange={(e) => setSize(Number(e.target.value))} />
              </label>
            </div>
          </div>
        )}

        <label className="mt-3 block text-xs font-medium text-slate-600">
          Maks. attempt (retry kegagalan sementara, dengan backoff)
          <input type="number" min={1} max={10} className="input-field mt-1 w-full sm:w-32" value={attempts} onChange={(e) => setAttempts(Number(e.target.value))} />
        </label>

        {error && <p className="mt-3 break-words rounded-lg bg-rose-50 p-2 text-xs text-rose-800">{error}</p>}

        <div className="mt-5 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <button type="button" className="btn-secondary" onClick={onClose} disabled={busy}>
            Batal
          </button>
          <button type="button" className="btn-primary" onClick={() => void submit()} disabled={busy}>
            <Play className="h-4 w-4" /> {busy ? 'Mempublikasikan…' : 'Publish job'}
          </button>
        </div>
      </div>
    </div>
  );
}
