import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Database, FileCode2, RefreshCw, Table2 } from 'lucide-react';
import { viewerApi } from '../api/endpoints';
import { ApiError } from '../api/client';
import type { AccessibleDatabase } from '../types/api';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';

export function DatabasesPage() {
  const [databases, setDatabases] = useState<AccessibleDatabase[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const res = await viewerApi.listDatabases();
      setDatabases(res.databases ?? []);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Gagal memuat daftar database.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <PageHeader
        title="Databases & Viewer"
        description="Pilih database target untuk melihat struktur skema, data tabel (read-only), dan riwayat migration."
        actions={
          <button type="button" onClick={() => void load()} className="btn-secondary">
            <RefreshCw className="h-4 w-4" />
            Muat Ulang
          </button>
        }
      />

      {loading && <LoadingState label="Memuat daftar database..." />}
      {!loading && error && <ErrorState message={error} onRetry={() => void load()} />}
      {!loading && !error && databases.length === 0 && (
        <EmptyState
          title="Belum ada database yang dapat diakses"
          description="Hak akses database diberikan melalui group. Minta Platform Admin menambahkan kamu ke group yang memiliki akses ke database PostgreSQL."
        />
      )}

      {!loading && !error && databases.length > 0 && (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
          {databases.map((db) => (
            <div key={db.db_id} className="card flex flex-col justify-between p-5">
              <div>
                <div className="flex items-start justify-between gap-2">
                  <div className="flex items-center gap-2.5 min-w-0">
                    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600">
                      <Database className="h-5 w-5" />
                    </div>
                    <div className="min-w-0">
                      <h2 className="truncate text-base font-semibold text-slate-900">{db.name}</h2>
                      <p className="text-xs uppercase text-slate-500">
                        {db.engine || 'postgres'} · ID #{db.db_id}
                      </p>
                    </div>
                  </div>
                  <Badge variant={db.level === 2 ? 'emerald' : 'sky'}>{db.level_label}</Badge>
                </div>

                <p className="mt-3 text-xs text-slate-600">
                  {db.level === 2
                    ? 'Akses penuh untuk melihat skema/data serta mengajukan dan menjalankan migration.'
                    : 'Akses baca saja untuk eksplorasi skema, data tabel, dan riwayat migration.'}
                </p>
              </div>

              <div className="mt-5 flex flex-wrap gap-2 border-t border-slate-100 pt-4">
                <Link to={`/databases/${db.db_id}/schema`} className="btn-primary flex-1 text-xs">
                  <Table2 className="h-3.5 w-3.5" />
                  Schema & Data
                </Link>
                <Link
                  to={`/databases/${db.db_id}/migrations`}
                  className="btn-secondary flex-1 text-xs"
                >
                  <FileCode2 className="h-3.5 w-3.5" />
                  Migrations
                </Link>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
