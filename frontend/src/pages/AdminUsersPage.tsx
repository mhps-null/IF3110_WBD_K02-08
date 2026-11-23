import React, { useCallback, useEffect, useState } from 'react';
import { KeyRound, Shield, ShieldOff, UserCheck, UserPlus, UserX } from 'lucide-react';
import { adminApi } from '../api/endpoints';
import { ApiError } from '../api/client';
import type { AdminUser } from '../types/api';
import { useAuth } from '../context/AuthContext';
import { useToast } from '../context/ToastContext';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';
import { ConfirmDialog } from '../components/shared/ConfirmDialog';

export function AdminUsersPage() {
  const { user: currentUser } = useAuth();
  const { notify } = useToast();

  const [users, setUsers] = useState<AdminUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  // Create user form
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [creating, setCreating] = useState(false);

  // Reset password modal
  const [resetTarget, setResetTarget] = useState<AdminUser | null>(null);
  const [newPassword, setNewPassword] = useState('');
  const [resetting, setResetting] = useState(false);

  // Confirm dialog
  const [confirmState, setConfirmState] = useState<{
    title: string;
    description: string;
    destructive: boolean;
    action: () => Promise<void>;
  } | null>(null);
  const [actionBusy, setActionBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const res = await adminApi.listUsers();
      setUsers(res.users ?? []);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Gagal memuat daftar user.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!username.trim() || password.length < 8) {
      notify('error', 'Username wajib diisi dan password minimal 8 karakter.');
      return;
    }
    setCreating(true);
    try {
      await adminApi.createUser(username.trim(), password);
      notify('success', `User "${username.trim()}" berhasil dibuat.`);
      setUsername('');
      setPassword('');
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal membuat user.');
    } finally {
      setCreating(false);
    }
  };

  const handleResetPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!resetTarget || newPassword.length < 8) {
      notify('error', 'Password baru minimal 8 karakter.');
      return;
    }
    setResetting(true);
    try {
      const res = await adminApi.resetPassword(resetTarget.id, newPassword);
      notify('success', res.message);
      setResetTarget(null);
      setNewPassword('');
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal mereset password.');
    } finally {
      setResetting(false);
    }
  };

  const runConfirmed = async () => {
    if (!confirmState) return;
    setActionBusy(true);
    try {
      await confirmState.action();
      setConfirmState(null);
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Operasi gagal.');
    } finally {
      setActionBusy(false);
    }
  };

  return (
    <div>
      <PageHeader
        title="Manajemen Users"
        description="Kelola akun pengguna, reset password (mencabut seluruh sesi aktif), status aktif, dan hak Platform Admin."
      />

      {/* Create User Form */}
      <div className="card mb-6 p-5">
        <h2 className="mb-3 flex items-center gap-2 text-sm font-semibold text-slate-900">
          <UserPlus className="h-4 w-4 text-indigo-600" />
          Tambah User Baru
        </h2>
        <form onSubmit={handleCreate} className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <div>
            <label htmlFor="new-username" className="mb-1 block text-xs font-medium text-slate-600">
              Username
            </label>
            <input
              id="new-username"
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="contoh: alice"
              className="input-field"
              required
            />
          </div>
          <div>
            <label htmlFor="new-password" className="mb-1 block text-xs font-medium text-slate-600">
              Password (min. 8 karakter)
            </label>
            <input
              id="new-password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="••••••••"
              className="input-field"
              minLength={8}
              required
            />
          </div>
          <div className="flex items-end">
            <button type="submit" disabled={creating} className="btn-primary w-full sm:w-auto">
              <UserPlus className="h-4 w-4" />
              {creating ? 'Menyimpan...' : 'Buat User'}
            </button>
          </div>
        </form>
      </div>

      {loading && <LoadingState label="Memuat daftar user..." />}
      {!loading && error && <ErrorState message={error} onRetry={() => void load()} />}
      {!loading && !error && users.length === 0 && (
        <EmptyState title="Belum ada user" description="Buat akun pengguna baru pada form di atas." />
      )}

      {!loading && !error && users.length > 0 && (
        <div className="card overflow-hidden">
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
              <thead className="bg-slate-50 text-xs font-semibold uppercase text-slate-500">
                <tr>
                  <th className="px-4 py-3">Username</th>
                  <th className="px-4 py-3">Peran</th>
                  <th className="px-4 py-3">Status</th>
                  <th className="px-4 py-3">Dibuat</th>
                  <th className="px-4 py-3 text-right">Aksi</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 bg-white">
                {users.map((u) => {
                  const isSelf = currentUser?.id === u.id;
                  return (
                    <tr key={u.id} className="hover:bg-slate-50/70">
                      <td className="whitespace-nowrap px-4 py-3 font-medium text-slate-900">
                        {u.username}
                        {isSelf && (
                          <span className="ml-2 text-xs text-slate-400">(Kamu)</span>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        {u.is_admin ? (
                          <Badge variant="indigo">Platform Admin</Badge>
                        ) : (
                          <Badge variant="neutral">User Biasa</Badge>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        {u.is_active ? (
                          <Badge variant="emerald">Aktif</Badge>
                        ) : (
                          <Badge variant="rose">Nonaktif</Badge>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-xs text-slate-500">
                        {new Date(u.created_at).toLocaleString('id-ID')}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-right">
                        <div className="inline-flex flex-wrap justify-end gap-1.5">
                          <button
                            type="button"
                            onClick={() => {
                              setResetTarget(u);
                              setNewPassword('');
                            }}
                            className="btn-secondary px-2.5 py-1.5 text-xs"
                          >
                            <KeyRound className="h-3.5 w-3.5" />
                            Reset Password
                          </button>

                          <button
                            type="button"
                            disabled={isSelf && u.is_admin}
                            onClick={() =>
                              setConfirmState({
                                title: u.is_admin ? 'Cabut Hak Platform Admin?' : 'Jadikan Platform Admin?',
                                description: `Perubahan peran untuk "${u.username}" akan mencabut seluruh sesi aktif user tersebut.`,
                                destructive: u.is_admin,
                                action: async () => {
                                  const res = await adminApi.toggleAdmin(u.id, !u.is_admin);
                                  notify('success', res.message);
                                },
                              })
                            }
                            className="btn-secondary px-2.5 py-1.5 text-xs"
                          >
                            {u.is_admin ? (
                              <>
                                <ShieldOff className="h-3.5 w-3.5" />
                                Cabut Admin
                              </>
                            ) : (
                              <>
                                <Shield className="h-3.5 w-3.5" />
                                Jadikan Admin
                              </>
                            )}
                          </button>

                          {u.is_active ? (
                            <button
                              type="button"
                              disabled={isSelf}
                              onClick={() =>
                                setConfirmState({
                                  title: `Nonaktifkan "${u.username}"?`,
                                  description:
                                    'Seluruh sesi aktif user akan langsung dicabut dan user tidak dapat masuk kembali hingga diaktifkan lagi.',
                                  destructive: true,
                                  action: async () => {
                                    const res = await adminApi.deactivateUser(u.id);
                                    notify('success', res.message);
                                  },
                                })
                              }
                              className="btn-danger px-2.5 py-1.5 text-xs"
                            >
                              <UserX className="h-3.5 w-3.5" />
                              Nonaktifkan
                            </button>
                          ) : (
                            <button
                              type="button"
                              onClick={() =>
                                setConfirmState({
                                  title: `Aktifkan "${u.username}"?`,
                                  description:
                                    'User dapat masuk kembali dengan login baru. Session lama tetap tidak aktif.',
                                  destructive: false,
                                  action: async () => {
                                    const res = await adminApi.activateUser(u.id);
                                    notify('success', res.message);
                                  },
                                })
                              }
                              className="btn-secondary px-2.5 py-1.5 text-xs text-emerald-700"
                            >
                              <UserCheck className="h-3.5 w-3.5" />
                              Aktifkan
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Reset Password Modal */}
      {resetTarget && (
        <div
          role="dialog"
          aria-modal="true"
          className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4"
        >
          <div className="card w-full max-w-md p-6">
            <h2 className="text-base font-semibold text-slate-900">
              Reset Password: {resetTarget.username}
            </h2>
            <p className="mt-1 text-xs text-slate-600">
              Reset password akan mencabut seluruh sesi aktif milik user ini.
            </p>
            <form onSubmit={handleResetPassword} className="mt-4 space-y-4">
              <div>
                <label htmlFor="reset-pass" className="mb-1 block text-xs font-medium text-slate-700">
                  Password Baru (min. 8 karakter)
                </label>
                <input
                  id="reset-pass"
                  type="password"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  className="input-field"
                  minLength={8}
                  required
                />
              </div>
              <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
                <button
                  type="button"
                  onClick={() => setResetTarget(null)}
                  className="btn-secondary"
                >
                  Batal
                </button>
                <button type="submit" disabled={resetting} className="btn-primary">
                  {resetting ? 'Menyimpan...' : 'Simpan Password Baru'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={Boolean(confirmState)}
        title={confirmState?.title ?? ''}
        description={confirmState?.description ?? ''}
        destructive={confirmState?.destructive}
        loading={actionBusy}
        onConfirm={() => void runConfirmed()}
        onCancel={() => setConfirmState(null)}
      />
    </div>
  );
}
