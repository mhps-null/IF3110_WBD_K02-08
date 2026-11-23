import React, { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { AlertTriangle, ArrowLeft, Crown, Save, UserMinus, UserPlus } from 'lucide-react';
import { adminApi } from '../api/endpoints';
import { ApiError } from '../api/client';
import type { AdminUser, GroupDetailResponse } from '../types/api';
import { useToast } from '../context/ToastContext';
import { Badge, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';
import { ConfirmDialog } from '../components/shared/ConfirmDialog';

export function AdminGroupDetailPage() {
  const { id } = useParams<{ id: string }>();
  const groupId = Number(id);
  const { notify } = useToast();

  const [detail, setDetail] = useState<GroupDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [savingGroup, setSavingGroup] = useState(false);

  const [selectedUserId, setSelectedUserId] = useState('');
  const [addingMember, setAddingMember] = useState(false);

  const [selectedLeaderId, setSelectedLeaderId] = useState('');
  const [savingLeader, setSavingLeader] = useState(false);

  const [removeTarget, setRemoveTarget] = useState<AdminUser | null>(null);
  const [removing, setRemoving] = useState(false);

  const load = useCallback(async () => {
    if (!Number.isInteger(groupId) || groupId <= 0) {
      setError('ID group tidak valid.');
      setLoading(false);
      return;
    }
    setLoading(true);
    setError('');
    try {
      const res = await adminApi.getGroup(groupId);
      setDetail(res);
      setName(res.group.name);
      setDescription(res.group.description);
      setSelectedLeaderId(res.group.leader_user_id ? String(res.group.leader_user_id) : '');
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Gagal memuat detail group.');
    } finally {
      setLoading(false);
    }
  }, [groupId]);

  useEffect(() => {
    void load();
  }, [load]);

  const handleSaveGroup = async (e: React.FormEvent) => {
    e.preventDefault();
    setSavingGroup(true);
    try {
      const res = await adminApi.updateGroup(groupId, name.trim(), description.trim());
      notify('success', res.message);
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal memperbarui group.');
    } finally {
      setSavingGroup(false);
    }
  };

  const handleAddMember = async (e: React.FormEvent) => {
    e.preventDefault();
    const uid = Number(selectedUserId);
    if (!uid) return;
    setAddingMember(true);
    try {
      const res = await adminApi.addMember(groupId, uid);
      notify('success', res.message);
      setSelectedUserId('');
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal menambahkan anggota.');
    } finally {
      setAddingMember(false);
    }
  };

  const handleRemoveMember = async () => {
    if (!removeTarget) return;
    setRemoving(true);
    try {
      const res = await adminApi.removeMember(groupId, removeTarget.id);
      notify('success', res.message);
      setRemoveTarget(null);
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal menghapus anggota.');
    } finally {
      setRemoving(false);
    }
  };

  const handleSaveLeader = async (e: React.FormEvent) => {
    e.preventDefault();
    setSavingLeader(true);
    try {
      const leaderId = selectedLeaderId === '' ? null : Number(selectedLeaderId);
      const res = await adminApi.setGroupLeader(groupId, leaderId);
      notify('success', res.message);
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal memperbarui leader group.');
    } finally {
      setSavingLeader(false);
    }
  };

  const activeMembers = (detail?.members ?? []).filter((m) => m.is_active);

  return (
    <div>
      <div className="mb-3">
        <Link
          to="/admin/groups"
          className="inline-flex items-center gap-1.5 text-sm font-medium text-slate-600 hover:text-indigo-600"
        >
          <ArrowLeft className="h-4 w-4" />
          Kembali ke Daftar Groups
        </Link>
      </div>

      <PageHeader
        title={detail ? `Group: ${detail.group.name}` : `Detail Group #${groupId}`}
        description="Ubah informasi group, kelola daftar anggota, dan tetapkan Group Leader dari anggota aktif."
      />

      {loading && <LoadingState label="Memuat detail group..." />}
      {!loading && error && <ErrorState message={error} onRetry={() => void load()} />}

      {!loading && !error && detail && (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-12">
          {/* Edit group & assign leader */}
          <div className="space-y-6 lg:col-span-5">
            <section className="card p-5">
              <h2 className="mb-4 text-sm font-semibold text-slate-900">Informasi Group</h2>
              <form onSubmit={handleSaveGroup} className="space-y-3">
                <div>
                  <label htmlFor="edit-g-name" className="mb-1 block text-xs font-medium text-slate-600">
                    Nama Group
                  </label>
                  <input
                    id="edit-g-name"
                    type="text"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    className="input-field"
                    maxLength={100}
                    required
                  />
                </div>
                <div>
                  <label htmlFor="edit-g-desc" className="mb-1 block text-xs font-medium text-slate-600">
                    Deskripsi
                  </label>
                  <textarea
                    id="edit-g-desc"
                    rows={3}
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    className="input-field"
                  />
                </div>
                <button type="submit" disabled={savingGroup} className="btn-primary text-xs">
                  <Save className="h-3.5 w-3.5" />
                  {savingGroup ? 'Menyimpan...' : 'Simpan Perubahan'}
                </button>
              </form>
            </section>

            {/* Group Leader Management */}
            <section className="card p-5">
              <div className="mb-3 flex items-center justify-between gap-2">
                <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900">
                  <Crown className="h-4 w-4 text-amber-500" />
                  Group Leader
                </h2>
                {detail.group.has_leader ? (
                  <Badge variant="emerald">Leader Aktif</Badge>
                ) : (
                  <Badge variant="amber">Belum Ditetapkan (Pending)</Badge>
                )}
              </div>

              {!detail.group.has_leader && (
                <div className="mb-4 flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900">
                  <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
                  <p>
                    Group ini belum memiliki leader aktif. Sesuai aturan M2, pengajuan migration pada
                    database yang diakses group ini akan tetap berstatus <strong>Pending</strong>{' '}
                    (bukan ditolak) sampai leader ditetapkan.
                  </p>
                </div>
              )}

              <form onSubmit={handleSaveLeader} className="space-y-3">
                <div>
                  <label htmlFor="leader-select" className="mb-1 block text-xs font-medium text-slate-600">
                    Pilih Leader (harus anggota aktif group)
                  </label>
                  <select
                    id="leader-select"
                    value={selectedLeaderId}
                    onChange={(e) => setSelectedLeaderId(e.target.value)}
                    className="input-field"
                  >
                    <option value="">— Tanpa Leader (Pending) —</option>
                    {activeMembers.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.username}
                      </option>
                    ))}
                  </select>
                  <p className="mt-1.5 text-xs text-slate-500">
                    Satu user diperbolehkan menjadi leader di beberapa group sekaligus.
                  </p>
                </div>
                <button type="submit" disabled={savingLeader} className="btn-primary text-xs">
                  <Crown className="h-3.5 w-3.5" />
                  {savingLeader ? 'Menyimpan...' : 'Simpan Leader'}
                </button>
              </form>
            </section>
          </div>

          {/* Membership */}
          <div className="lg:col-span-7">
            <section className="card p-5">
              <h2 className="mb-4 text-sm font-semibold text-slate-900">
                Anggota Group ({detail.members.length})
              </h2>

              <form onSubmit={handleAddMember} className="mb-5 flex flex-col gap-2 sm:flex-row">
                <select
                  aria-label="Pilih user untuk ditambahkan"
                  value={selectedUserId}
                  onChange={(e) => setSelectedUserId(e.target.value)}
                  className="input-field flex-1"
                >
                  <option value="">Pilih user untuk ditambahkan...</option>
                  {detail.available_users.map((u) => (
                    <option key={u.id} value={u.id}>
                      {u.username} {!u.is_active ? '(Nonaktif)' : ''}
                    </option>
                  ))}
                </select>
                <button
                  type="submit"
                  disabled={!selectedUserId || addingMember}
                  className="btn-primary shrink-0 text-xs"
                >
                  <UserPlus className="h-4 w-4" />
                  {addingMember ? 'Menambahkan...' : 'Tambah Anggota'}
                </button>
              </form>

              {detail.members.length === 0 ? (
                <p className="rounded-lg border border-dashed border-slate-200 p-6 text-center text-sm text-slate-500">
                  Belum ada anggota di group ini. Tambahkan minimal satu anggota aktif agar dapat
                  ditetapkan sebagai leader.
                </p>
              ) : (
                <ul className="divide-y divide-slate-100">
                  {detail.members.map((m) => {
                    const isLeader = detail.group.leader_user_id === m.id;
                    return (
                      <li key={m.id} className="flex items-center justify-between gap-3 py-3">
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-2">
                            <span className="truncate text-sm font-medium text-slate-900">
                              {m.username}
                            </span>
                            {isLeader && (
                              <Badge variant="amber">
                                <Crown className="h-3 w-3" />
                                Leader
                              </Badge>
                            )}
                            {!m.is_active && <Badge variant="rose">Nonaktif</Badge>}
                          </div>
                        </div>
                        <button
                          type="button"
                          onClick={() => setRemoveTarget(m)}
                          className="btn-secondary px-2.5 py-1.5 text-xs text-rose-600 hover:bg-rose-50"
                        >
                          <UserMinus className="h-3.5 w-3.5" />
                          Keluarkan
                        </button>
                      </li>
                    );
                  })}
                </ul>
              )}
            </section>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={Boolean(removeTarget)}
        title={`Keluarkan "${removeTarget?.username ?? ''}" dari group?`}
        description={
          removeTarget && detail?.group.leader_user_id === removeTarget.id
            ? 'User ini sedang menjabat sebagai Group Leader. Mengeluarkannya akan mengosongkan leader group ini (workflow approval akan menjadi Pending).'
            : 'Hak akses database yang berasal dari group ini akan langsung dicabut dari user tersebut.'
        }
        confirmLabel="Keluarkan"
        destructive
        loading={removing}
        onConfirm={() => void handleRemoveMember()}
        onCancel={() => setRemoveTarget(null)}
      />
    </div>
  );
}
