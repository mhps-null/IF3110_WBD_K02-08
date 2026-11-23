import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { AlertTriangle, Crown, FileCode2, Info, Table2 } from 'lucide-react';
import { adminApi, migrationsApi } from '../api/endpoints';
import { ApiError } from '../api/client';
import { useAuth } from '../context/AuthContext';
import type { DatabaseAccessGroupsResponse, MigrationRecord } from '../types/api';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';

export function MigrationsShellPage() {
  const { id } = useParams<{ id?: string }>();
  const navigate = useNavigate();
  const { accessibleDatabases } = useAuth();

  const selectedDbId = id ? Number(id) : accessibleDatabases[0]?.db_id ?? 0;

  const [migrations, setMigrations] = useState<MigrationRecord[]>([]);
  const [approvers, setApprovers] = useState<DatabaseAccessGroupsResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    if (!selectedDbId || selectedDbId <= 0) return;
    setLoading(true);
    setError('');
    try {
      const [migRes, appRes] = await Promise.all([
        migrationsApi.listByDatabase(selectedDbId),
        adminApi.getDatabaseAccessGroups(selectedDbId).catch(() => null),
      ]);
      setMigrations(migRes.migrations ?? []);
      setApprovers(appRes);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Gagal memuat daftar migration.');
    } finally {
      setLoading(false);
    }
  }, [selectedDbId]);

  useEffect(() => {
    void load();
  }, [load]);

  const currentDb = accessibleDatabases.find((d) => d.db_id === selectedDbId);

  return (
    <div>
      <PageHeader
        title="Migrations & History"
        description="Integrasi SPA Shell untuk Migration Editor (Up/Down/Irreversible), Approval Multi-Group, Rollback/Forward, dan Hide Rollback (domain Orang 3)."
        actions={
          accessibleDatabases.length > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              <select
                aria-label="Pilih database target"
                value={selectedDbId || ''}
                onChange={(e) => navigate(`/databases/${e.target.value}/migrations`)}
                className="input-field w-auto min-w-[200px]"
              >
                {accessibleDatabases.map((db) => (
                  <option key={db.db_id} value={db.db_id}>
                    {db.name} ({db.level_label})
                  </option>
                ))}
              </select>
              {selectedDbId > 0 && (
                <Link to={`/databases/${selectedDbId}/schema`} className="btn-secondary text-xs">
                  <Table2 className="h-3.5 w-3.5" />
                  Schema Viewer
                </Link>
              )}
            </div>
          )
        }
      />

      {/* Integration Contract Banner for Orang 3 */}
      <div className="mb-6 rounded-xl border border-indigo-200 bg-indigo-50/70 p-4 text-sm text-indigo-950">
        <div className="flex items-start gap-2.5">
          <Info className="mt-0.5 h-5 w-5 shrink-0 text-indigo-600" />
          <div className="space-y-1">
            <p className="font-semibold">Titik Integrasi Modul Orang 3 (Migration & Approval)</p>
            <p className="text-xs leading-relaxed text-indigo-900">
              Shell SPA ini sudah membaca riwayat M1 melalui{' '}
              <code className="rounded bg-white/80 px-1 py-0.5 font-mono">GET /api/databases/:id/migrations</code>{' '}
              dan approver group melalui{' '}
              <code className="rounded bg-white/80 px-1 py-0.5 font-mono">GET /api/databases/:id/access-groups</code>.
              Orang 3 dapat langsung menambahkan Migration Editor (SQL Up/Down/Irreversible), tombol
              Submit/Approve/Execute, Rollback/Forward, dan checkbox <strong>Hide rollback</strong> di
              halaman ini menggunakan <code className="rounded bg-white/80 px-1 py-0.5 font-mono">apiRequest()</code>.
            </p>
          </div>
        </div>
      </div>

      {accessibleDatabases.length === 0 ? (
        <EmptyState
          title="Tidak ada database target"
          description="Kamu belum memiliki akses ke database mana pun untuk melihat atau membuat migration."
        />
      ) : (
        <>
          {approvers && (
            <div
              className={`mb-6 rounded-xl border p-4 text-xs ${
                approvers.has_missing_leaders
                  ? 'border-amber-200 bg-amber-50 text-amber-900'
                  : 'border-slate-200 bg-white text-slate-700'
              }`}
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 font-semibold">
                  {approvers.has_missing_leaders ? (
                    <AlertTriangle className="h-4 w-4 text-amber-600" />
                  ) : (
                    <Crown className="h-4 w-4 text-amber-500" />
                  )}
                  Required Approvers untuk {currentDb?.name ?? `DB #${selectedDbId}`}
                </span>
                <Badge variant={approvers.has_missing_leaders ? 'amber' : 'emerald'}>
                  {approvers.has_missing_leaders ? 'Pending — Leader Belum Lengkap' : 'Semua Group Punya Leader'}
                </Badge>
              </div>
              {approvers.has_missing_leaders && (
                <p className="mt-1 text-amber-800">{approvers.pending_reason}</p>
              )}
            </div>
          )}

          {loading && <LoadingState label="Memuat riwayat migration..." />}
          {!loading && error && <ErrorState message={error} onRetry={() => void load()} />}
          {!loading && !error && migrations.length === 0 && (
            <EmptyState
              title="Belum ada migration pada database ini"
              description="Riwayat migration M1/M2 untuk database terpilih akan muncul di sini."
            />
          )}

          {!loading && !error && migrations.length > 0 && (
            <div className="space-y-3">
              {migrations.map((m) => (
                <article key={m.id} className="card p-4">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <FileCode2 className="h-4 w-4 text-indigo-600" />
                      <h3 className="text-sm font-semibold text-slate-900">
                        {m.version ? `v${m.version} — ` : ''}
                        {m.title}
                      </h3>
                    </div>
                    <Badge
                      variant={
                        m.status === 'success'
                          ? 'emerald'
                          : m.status === 'failed'
                            ? 'rose'
                            : 'neutral'
                      }
                    >
                      {m.status}
                    </Badge>
                  </div>
                  <p className="mt-1 text-xs text-slate-500">
                    Dibuat oleh {m.created_by_name || `#${m.created_by}`} ·{' '}
                    {new Date(m.created_at).toLocaleString('id-ID')}
                  </p>
                  <div className="mt-3 overflow-x-auto rounded-lg bg-slate-900 p-3 font-mono text-xs text-slate-100">
                    <pre className="whitespace-pre">{m.sql_text}</pre>
                  </div>
                </article>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}
