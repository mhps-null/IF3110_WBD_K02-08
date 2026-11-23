import React, { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle, Crown, Plus, Settings, Trash2, Users } from 'lucide-react';
import { adminApi } from '../api/endpoints';
import { ApiError } from '../api/client';
import type { AdminGroup } from '../types/api';
import { useToast } from '../context/ToastContext';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';
import { ConfirmDialog } from '../components/shared/ConfirmDialog';

export function AdminGroupsPage() {
  const { notify } = useToast();
  const [groups, setGroups] = useState<AdminGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [creating, setCreating] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<AdminGroup | null>(null);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const res = await adminApi.listGroups();
      setGroups(res.groups ?? []);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Gagal memuat daftar group.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      notify('error', 'Nama group wajib diisi.');
      return;
    }
    setCreating(true);
    try {
      await adminApi.createGroup(name.trim(), description.trim());
      notify('success', `Group "${name.trim()}" berhasil dibuat.`);
      setName('');
      setDescription('');
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal membuat group.');
    } finally {
      setCreating(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      const res = await adminApi.deleteGroup(deleteTarget.id);
      notify('success', res.message);
      setDeleteTarget(null);
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal menghapus group.');
    } finally {
      setDeleting(false);
    }
  };

  return (
    <div>
      <PageHeader
        title="Groups & Group Leaders"
        description="Kelola group pengembang, keanggotaan user (multi-group), serta penetapan leader untuk approval migration."
      />

      <div className="card mb-6 p-5">
        <h2 className="mb-3 flex items-center gap-2 text-sm font-semibold text-slate-900">
          <Plus className="h-4 w-4 text-indigo-600" />
          Buat Group Baru
        </h2>
        <form onSubmit={handleCreate} className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <div>
            <label htmlFor="group-name" className="mb-1 block text-xs font-medium text-slate-600">
              Nama Group
            </label>
            <input
              id="group-name"
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="contoh: Backend Team"
              className="input-field"
              maxLength={100}
              required
            />
          </div>
          <div>
            <label htmlFor="group-desc" className="mb-1 block text-xs font-medium text-slate-600">
              Deskripsi
            </label>
            <input
              id="group-desc"
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Tim pengembang layanan inti"
              className="input-field"
            />
          </div>
          <div className="flex items-end">
            <button type="submit" disabled={creating} className="btn-primary w-full sm:w-auto">
              <Plus className="h-4 w-4" />
              {creating ? 'Menyimpan...' : 'Buat Group'}
            </button>
          </div>
        </form>
      </div>

      {loading && <LoadingState label="Memuat daftar group..." />}
      {!loading && error && <ErrorState message={error} onRetry={() => void load()} />}
      {!loading && !error && groups.length === 0 && (
        <EmptyState
          title="Belum ada group"
          description="Buat group terlebih dahulu untuk mengatur anggota, leader, dan hak akses database."
        />
      )}

      {!loading && !error && groups.length > 0 && (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
          {groups.map((g) => (
            <div key={g.id} className="card flex flex-col justify-between p-5">
              <div>
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <h3 className="truncate text-base font-semibold text-slate-900">{g.name}</h3>
                    <p className="mt-1 line-clamp-2 text-xs text-slate-600">
                      {g.description || 'Tanpa deskripsi'}
                    </p>
                  </div>
                  <Badge variant="neutral">
                    <Users className="h-3 w-3" />
                    {g.member_count}
                  </Badge>
                </div>

                <div className="mt-4 rounded-lg border border-slate-100 bg-slate-50 px-3 py-2.5 text-xs">
                  {g.has_leader ? (
                    <div className="flex items-center gap-2 text-emerald-800">
                      <Crown className="h-4 w-4 shrink-0 text-amber-500" />
                      <span>
                        Leader: <strong>{g.leader_username}</strong>
                      </span>
                    </div>
                  ) : (
                    <div className="flex items-start gap-2 text-amber-800">
                      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
                      <span>
                        Belum ada leader aktif. Migration pada DB milik group ini akan tetap{' '}
                        <strong>Pending</strong>.
                      </span>
                    </div>
                  )}
                </div>
              </div>

              <div className="mt-4 flex items-center gap-2 border-t border-slate-100 pt-3">
                <Link to={`/admin/groups/${g.id}`} className="btn-primary flex-1 text-xs">
                  <Settings className="h-3.5 w-3.5" />
                  Kelola Anggota & Leader
                </Link>
                <button
                  type="button"
                  onClick={() => setDeleteTarget(g)}
                  aria-label={`Hapus group ${g.name}`}
                  className="btn-secondary px-2.5 py-2 text-rose-600 hover:bg-rose-50"
                >
                  <Trash2 className="h-4 w-4" />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={`Hapus group "${deleteTarget?.name ?? ''}"?`}
        description="Seluruh keanggotaan dan hak akses database milik group ini akan dihapus, serta privilege_version anggota akan diperbarui."
        confirmLabel="Hapus Group"
        destructive
        loading={deleting}
        onConfirm={() => void handleDelete()}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  );
}
