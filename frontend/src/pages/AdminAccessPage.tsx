import React, { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle, Crown, KeyRound, Save } from 'lucide-react';
import { adminApi } from '../api/endpoints';
import { ApiError } from '../api/client';
import type { AccessMatrixResponse } from '../types/api';
import { useToast } from '../context/ToastContext';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';

export function AdminAccessPage() {
  const { notify } = useToast();
  const [matrix, setMatrix] = useState<AccessMatrixResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [selectedDbId, setSelectedDbId] = useState('');
  const [selectedGroupId, setSelectedGroupId] = useState('');
  const [level, setLevel] = useState('1');
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const res = await adminApi.getAccessMatrix();
      setMatrix(res);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Gagal memuat matriks akses.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const dbId = Number(selectedDbId);
    const groupId = Number(selectedGroupId);
    if (!dbId || !groupId) {
      notify('error', 'Pilih database dan group terlebih dahulu.');
      return;
    }
    setSaving(true);
    try {
      const res = await adminApi.setAccess(groupId, dbId, Number(level));
      notify('success', res.message);
      await load();
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : 'Gagal menyimpan hak akses.');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <PageHeader
        title="Matriks Hak Akses Database"
        description="Tetapkan tingkat akses View Only atau View + Edit per group. Setiap group dengan akses (termasuk View Only) wajib memiliki leader untuk menyetujui migration."
      />

      {loading && <LoadingState label="Memuat matriks akses..." />}
      {!loading && error && <ErrorState message={error} onRetry={() => void load()} />}

      {!loading && !error && matrix && (
        <>
          <div className="card mb-6 p-5">
            <h2 className="mb-3 flex items-center gap-2 text-sm font-semibold text-slate-900">
              <KeyRound className="h-4 w-4 text-indigo-600" />
              Atur Hak Akses Group ke Database
            </h2>
            <form onSubmit={handleSave} className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <div>
                <label htmlFor="matrix-db" className="mb-1 block text-xs font-medium text-slate-600">
                  Database
                </label>
                <select
                  id="matrix-db"
                  value={selectedDbId}
                  onChange={(e) => setSelectedDbId(e.target.value)}
                  className="input-field"
                  required
                >
                  <option value="">Pilih database...</option>
                  {matrix.databases.map((d) => (
                    <option key={d.id} value={d.id}>
                      {d.name}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label htmlFor="matrix-group" className="mb-1 block text-xs font-medium text-slate-600">
                  Group
                </label>
                <select
                  id="matrix-group"
                  value={selectedGroupId}
                  onChange={(e) => setSelectedGroupId(e.target.value)}
                  className="input-field"
                  required
                >
                  <option value="">Pilih group...</option>
                  {matrix.groups.map((g) => (
                    <option key={g.id} value={g.id}>
                      {g.name} {g.has_leader ? `(Leader: ${g.leader_username})` : '(Belum ada leader)'}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label htmlFor="matrix-level" className="mb-1 block text-xs font-medium text-slate-600">
                  Tingkat Akses
                </label>
                <select
                  id="matrix-level"
                  value={level}
                  onChange={(e) => setLevel(e.target.value)}
                  className="input-field"
                >
                  <option value="1">View Only</option>
                  <option value="2">View + Edit</option>
                  <option value="0">Cabut Akses (None)</option>
                </select>
              </div>
              <div className="flex items-end">
                <button type="submit" disabled={saving} className="btn-primary w-full text-xs">
                  <Save className="h-4 w-4" />
                  {saving ? 'Menyimpan...' : 'Simpan Akses'}
                </button>
              </div>
            </form>
          </div>

          {matrix.access_entries.length === 0 ? (
            <EmptyState
              title="Belum ada hak akses terdaftar"
              description="Tetapkan minimal satu akses group ke database pada form di atas."
            />
          ) : (
            <div className="card overflow-hidden">
              <div className="overflow-x-auto">
                <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
                  <thead className="bg-slate-50 text-xs font-semibold uppercase text-slate-500">
                    <tr>
                      <th className="px-4 py-3">Database</th>
                      <th className="px-4 py-3">Group</th>
                      <th className="px-4 py-3">Tingkat Akses</th>
                      <th className="px-4 py-3">Status Leader (Approver)</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 bg-white">
                    {matrix.access_entries.map((entry) => (
                      <tr key={`${entry.db_id}-${entry.group_id}`} className="hover:bg-slate-50/70">
                        <td className="whitespace-nowrap px-4 py-3 font-medium text-slate-900">
                          {entry.database_name}
                        </td>
                        <td className="whitespace-nowrap px-4 py-3">
                          <Link
                            to={`/admin/groups/${entry.group_id}`}
                            className="font-medium text-indigo-600 hover:underline"
                          >
                            {entry.group_name}
                          </Link>
                        </td>
                        <td className="whitespace-nowrap px-4 py-3">
                          <Badge variant={entry.level === 2 ? 'emerald' : 'sky'}>
                            {entry.level_label}
                          </Badge>
                        </td>
                        <td className="whitespace-nowrap px-4 py-3 text-xs">
                          {entry.has_leader ? (
                            <span className="inline-flex items-center gap-1.5 text-emerald-700">
                              <Crown className="h-3.5 w-3.5 text-amber-500" />
                              {entry.leader_username}
                            </span>
                          ) : (
                            <span className="inline-flex items-center gap-1.5 font-medium text-amber-700">
                              <AlertTriangle className="h-3.5 w-3.5" />
                              Tanpa Leader — Approval Pending
                            </span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
