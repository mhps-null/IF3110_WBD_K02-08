import React, { useCallback, useEffect, useState } from 'react';
import { Activity, CheckCircle2, Pencil, PlugZap, Plus, Server, Trash2, XCircle } from 'lucide-react';
import { connectionsApi, type ConnectionPayload } from '../api/endpoints';
import { ApiError } from '../api/client';
import type { DBConnection, PoolStat } from '../types/api';
import { useAuth } from '../context/AuthContext';
import { useToast } from '../context/ToastContext';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';
import { ConfirmDialog } from '../components/shared/ConfirmDialog';

const emptyForm: ConnectionPayload = {
  name: '',
  engine: 'postgres',
  host: 'localhost',
  port: 5432,
  database_name: '',
  username: '',
  password: '',
  ssl_mode: 'disable',
};

export function AdminConnectionsPage() {
  const { refreshSession } = useAuth();
  const { notify } = useToast();
  const [connections, setConnections] = useState<DBConnection[]>([]);
  const [pools, setPools] = useState<PoolStat[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [editingId, setEditingId] = useState<number | null>(null);
  const [form, setForm] = useState<ConnectionPayload>(emptyForm);
  const [saving, setSaving] = useState(false);

  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null);

  const [deleteTarget, setDeleteTarget] = useState<DBConnection | null>(null);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [connRes, poolRes] = await Promise.all([
        connectionsApi.list(),
        connectionsApi.poolStats().catch(() => ({ pools: [] })),
      ]);
      setConnections(connRes.connections ?? []);
      setPools(poolRes.pools ?? []);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Gagal memuat daftar koneksi.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const startEdit = (c: DBConnection) => {
    setEditingId(c.id);
    setTestResult(null);
    setForm({
      id: c.id,
      name: c.name,
      engine: c.engine,
      host: c.host,
      port: c.port,
      database_name: c.database_name,
      username: c.username,
      password: '',
      ssl_mode: c.ssl_mode || 'disable',
    });
  };

  const resetForm = () => {
    setEditingId(null);
    setTestResult(null);
    setForm(emptyForm);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    try {
      if (editingId) {
        const res = await connectionsApi.update(editingId, form);
        notify('success', res.message);
      } else {
        const res = await connectionsApi.create(form);
        notify('success', res.message);
      }
      resetForm();
      await Promise.all([load(), refreshSession()]);
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal menyimpan koneksi.');
    } finally {
      setSaving(false);
    }
  };

  const handleTest = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const res = await connectionsApi.test({
        ...form,
        id: editingId ?? undefined,
      });
      setTestResult({ ok: res.success, msg: res.message });
    } catch (err) {
      setTestResult({
        ok: false,
        msg: err instanceof ApiError ? err.message : 'Koneksi gagal diuji.',
      });
    } finally {
      setTesting(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      const res = await connectionsApi.remove(deleteTarget.id);
      notify('success', res.message);
      setDeleteTarget(null);
      await Promise.all([load(), refreshSession()]);
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal menghapus koneksi.');
    } finally {
      setDeleting(false);
    }
  };

  return (
    <div>
      <PageHeader
        title="Manajemen Koneksi Database"
        description="Kelola profil koneksi PostgreSQL target (password terenkripsi AES-256-GCM di backend) dan pantau statistik connection pool."
      />

      {/* Form Create / Edit */}
      <div className="card mb-6 p-5">
        <div className="mb-4 flex items-center justify-between gap-2">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900">
            {editingId ? <Pencil className="h-4 w-4 text-indigo-600" /> : <Plus className="h-4 w-4 text-indigo-600" />}
            {editingId ? `Ubah Koneksi #${editingId}` : 'Tambah Koneksi Database Baru'}
          </h2>
          {editingId && (
            <button type="button" onClick={resetForm} className="btn-secondary text-xs">
              Batal Ubah
            </button>
          )}
        </div>

        <form onSubmit={handleSubmit} className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div>
            <label htmlFor="conn-name" className="mb-1 block text-xs font-medium text-slate-600">
              Nama Profil
            </label>
            <input
              id="conn-name"
              type="text"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder="prod-ecommerce-pg"
              className="input-field"
              required
            />
          </div>
          <div>
            <label htmlFor="conn-engine" className="mb-1 block text-xs font-medium text-slate-600">
              Engine (Target M2: PostgreSQL)
            </label>
            <select
              id="conn-engine"
              value={form.engine}
              onChange={(e) => {
                const engine = e.target.value as ConnectionPayload['engine'];
                setForm({
                  ...form,
                  engine,
                  port: engine === 'postgres' ? 5432 : 3306,
                });
              }}
              className="input-field"
            >
              <option value="postgres">PostgreSQL (Wajib M2)</option>
              <option value="mysql">MySQL (Legacy M1)</option>
              <option value="mariadb">MariaDB (Legacy M1)</option>
            </select>
          </div>
          <div>
            <label htmlFor="conn-host" className="mb-1 block text-xs font-medium text-slate-600">
              Host
            </label>
            <input
              id="conn-host"
              type="text"
              value={form.host}
              onChange={(e) => setForm({ ...form, host: e.target.value })}
              className="input-field"
              required
            />
          </div>
          <div>
            <label htmlFor="conn-port" className="mb-1 block text-xs font-medium text-slate-600">
              Port
            </label>
            <input
              id="conn-port"
              type="number"
              min={1}
              max={65535}
              value={form.port}
              onChange={(e) => setForm({ ...form, port: Number(e.target.value) })}
              className="input-field"
              required
            />
          </div>
          <div>
            <label htmlFor="conn-db" className="mb-1 block text-xs font-medium text-slate-600">
              Nama Database
            </label>
            <input
              id="conn-db"
              type="text"
              value={form.database_name}
              onChange={(e) => setForm({ ...form, database_name: e.target.value })}
              className="input-field"
              required
            />
          </div>
          <div>
            <label htmlFor="conn-user" className="mb-1 block text-xs font-medium text-slate-600">
              Username
            </label>
            <input
              id="conn-user"
              type="text"
              value={form.username}
              onChange={(e) => setForm({ ...form, username: e.target.value })}
              className="input-field"
              required
            />
          </div>
          <div>
            <label htmlFor="conn-pass" className="mb-1 block text-xs font-medium text-slate-600">
              Password {editingId ? '(kosongkan jika tidak diubah)' : ''}
            </label>
            <input
              id="conn-pass"
              type="password"
              value={form.password}
              onChange={(e) => setForm({ ...form, password: e.target.value })}
              className="input-field"
              required={!editingId}
            />
          </div>
          <div>
            <label htmlFor="conn-ssl" className="mb-1 block text-xs font-medium text-slate-600">
              SSL Mode
            </label>
            <select
              id="conn-ssl"
              value={form.ssl_mode}
              onChange={(e) => setForm({ ...form, ssl_mode: e.target.value })}
              className="input-field"
            >
              <option value="disable">disable</option>
              <option value="require">require</option>
              <option value="verify-ca">verify-ca</option>
              <option value="verify-full">verify-full</option>
            </select>
          </div>

          <div className="flex flex-wrap items-center gap-2 pt-1 sm:col-span-2 lg:col-span-4">
            <button type="submit" disabled={saving} className="btn-primary text-xs">
              <Server className="h-4 w-4" />
              {saving ? 'Menyimpan...' : editingId ? 'Simpan Perubahan' : 'Simpan Koneksi'}
            </button>
            <button
              type="button"
              disabled={testing}
              onClick={() => void handleTest()}
              className="btn-secondary text-xs"
            >
              <PlugZap className="h-4 w-4" />
              {testing ? 'Menguji...' : 'Test Koneksi'}
            </button>
            {testResult && (
              <span
                className={`inline-flex items-center gap-1.5 text-xs font-medium ${
                  testResult.ok ? 'text-emerald-700' : 'text-rose-700'
                }`}
              >
                {testResult.ok ? (
                  <CheckCircle2 className="h-4 w-4" />
                ) : (
                  <XCircle className="h-4 w-4" />
                )}
                {testResult.msg}
              </span>
            )}
          </div>
        </form>
      </div>

      {loading && <LoadingState label="Memuat daftar koneksi..." />}
      {!loading && error && <ErrorState message={error} onRetry={() => void load()} />}
      {!loading && !error && connections.length === 0 && (
        <EmptyState
          title="Belum ada profil koneksi"
          description="Tambahkan koneksi PostgreSQL di atas agar group dapat diberikan akses."
        />
      )}

      {!loading && !error && connections.length > 0 && (
        <div className="card overflow-hidden">
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
              <thead className="bg-slate-50 text-xs font-semibold uppercase text-slate-500">
                <tr>
                  <th className="px-4 py-3">Nama Profil</th>
                  <th className="px-4 py-3">Engine</th>
                  <th className="px-4 py-3">Host & Port</th>
                  <th className="px-4 py-3">Database / User</th>
                  <th className="px-4 py-3 text-right">Aksi</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 bg-white">
                {connections.map((c) => (
                  <tr key={c.id} className="hover:bg-slate-50/70">
                    <td className="whitespace-nowrap px-4 py-3 font-medium text-slate-900">
                      {c.name}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3">
                      <Badge variant={c.engine === 'postgres' ? 'indigo' : 'neutral'}>
                        {c.engine.toUpperCase()}
                      </Badge>
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-700">
                      {c.host}:{c.port}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-600">
                      {c.database_name} ({c.username})
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-right">
                      <div className="inline-flex gap-1.5">
                        <button
                          type="button"
                          onClick={() => startEdit(c)}
                          className="btn-secondary px-2.5 py-1.5 text-xs"
                        >
                          <Pencil className="h-3.5 w-3.5" />
                          Ubah
                        </button>
                        <button
                          type="button"
                          onClick={() => setDeleteTarget(c)}
                          className="btn-danger px-2.5 py-1.5 text-xs"
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                          Hapus
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Connection Pool Telemetry */}
      <div className="card mt-6 p-5">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900">
            <Activity className="h-4 w-4 text-emerald-600" />
            Active Connection Pool Stats
          </h2>
          <Badge variant="neutral">{pools.length} pool aktif</Badge>
        </div>
        {pools.length === 0 ? (
          <p className="text-xs text-slate-500">
            Belum ada target database pool yang terbuka pada server (pool dibuka lazy saat viewer/migration diakses).
          </p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-slate-200">
            <table className="min-w-full divide-y divide-slate-200 text-left text-xs">
              <thead className="bg-slate-50 font-semibold uppercase text-slate-500">
                <tr>
                  <th className="px-3.5 py-2">Database</th>
                  <th className="px-3.5 py-2">Open</th>
                  <th className="px-3.5 py-2">Active (In Use)</th>
                  <th className="px-3.5 py-2">Idle</th>
                  <th className="px-3.5 py-2">Wait Count</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 bg-white font-mono">
                {pools.map((p) => (
                  <tr key={p.db_id}>
                    <td className="px-3.5 py-2 font-sans font-medium text-slate-900">{p.db_name}</td>
                    <td className="px-3.5 py-2">{p.open_conns}</td>
                    <td className="px-3.5 py-2 text-indigo-700">{p.in_use}</td>
                    <td className="px-3.5 py-2 text-emerald-700">{p.idle}</td>
                    <td className="px-3.5 py-2">{p.wait_count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={`Hapus koneksi "${deleteTarget?.name ?? ''}"?`}
        description="Koneksi yang masih memiliki riwayat migration akan otomatis ditolak untuk menjaga integritas audit."
        confirmLabel="Hapus Koneksi"
        destructive
        loading={deleting}
        onConfirm={() => void handleDelete()}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  );
}
