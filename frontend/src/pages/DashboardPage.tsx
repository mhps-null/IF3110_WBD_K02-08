import { Link } from 'react-router-dom';
import {
  Activity,
  ArrowRight,
  CheckSquare,
  Crown,
  Database,
  FileCode2,
  KeyRound,
  ScrollText,
  Server,
  Shield,
  Users,
} from 'lucide-react';
import { useAuth } from '../context/AuthContext';
import { Badge, PageHeader } from '../components/shared/UIStates';

export function DashboardPage() {
  const { user, groups, accessibleDatabases } = useAuth();
  const leaderGroups = groups.filter((g) => g.is_leader);

  return (
    <div>
      <PageHeader
        title={`Selamat datang, ${user?.username ?? ''}`}
        description="Ringkasan hak akses database, keanggotaan group, dan modul operasional platform."
      />

      {/* Summary cards */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div className="card p-5">
          <div className="flex items-center justify-between">
            <p className="text-xs font-semibold uppercase tracking-wide text-slate-500">
              Database Dapat Diakses
            </p>
            <Database className="h-5 w-5 text-indigo-600" />
          </div>
          <p className="mt-2 text-2xl font-bold text-slate-900">{accessibleDatabases.length}</p>
          <p className="mt-1 text-xs text-slate-500">Berdasarkan keanggotaan group aktif</p>
        </div>

        <div className="card p-5">
          <div className="flex items-center justify-between">
            <p className="text-xs font-semibold uppercase tracking-wide text-slate-500">
              Keanggotaan Group
            </p>
            <Users className="h-5 w-5 text-sky-600" />
          </div>
          <p className="mt-2 text-2xl font-bold text-slate-900">{groups.length}</p>
          <p className="mt-1 text-xs text-slate-500">Multi-group efektif memakai hak tertinggi</p>
        </div>

        <div className="card p-5">
          <div className="flex items-center justify-between">
            <p className="text-xs font-semibold uppercase tracking-wide text-slate-500">
              Peran Group Leader
            </p>
            <Crown className="h-5 w-5 text-amber-500" />
          </div>
          <p className="mt-2 text-2xl font-bold text-slate-900">{leaderGroups.length}</p>
          <p className="mt-1 text-xs text-slate-500">Group yang dapat kamu approve</p>
        </div>

        <div className="card p-5">
          <div className="flex items-center justify-between">
            <p className="text-xs font-semibold uppercase tracking-wide text-slate-500">
              Peran Platform
            </p>
            <Shield className="h-5 w-5 text-emerald-600" />
          </div>
          <p className="mt-2 text-lg font-bold text-slate-900">
            {user?.is_platform_admin ? 'Platform Admin' : 'Standard User'}
          </p>
          <p className="mt-1 text-xs text-slate-500">
            {user?.is_platform_admin
              ? 'Kelola user, group, koneksi & audit'
              : 'Akses database ditentukan lewat group'}
          </p>
        </div>
      </div>

      <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
        {/* Accessible Databases */}
        <section className="card p-5">
          <div className="mb-4 flex items-center justify-between">
            <h2 className="text-base font-semibold text-slate-900">Database Target Kamu</h2>
            <Link
              to="/databases"
              className="inline-flex items-center gap-1 text-xs font-medium text-indigo-600 hover:text-indigo-700"
            >
              Lihat semua
              <ArrowRight className="h-3.5 w-3.5" />
            </Link>
          </div>
          {accessibleDatabases.length === 0 ? (
            <p className="text-sm text-slate-500">
              Kamu belum memiliki akses database melalui group mana pun.
            </p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {accessibleDatabases.slice(0, 6).map((db) => (
                <li key={db.db_id} className="flex items-center justify-between gap-3 py-3">
                  <div className="min-w-0">
                    <Link
                      to={`/databases/${db.db_id}/schema`}
                      className="truncate text-sm font-medium text-slate-900 hover:text-indigo-600"
                    >
                      {db.name}
                    </Link>
                    {db.engine && (
                      <p className="text-xs uppercase text-slate-500">{db.engine}</p>
                    )}
                  </div>
                  <Badge variant={db.level === 2 ? 'emerald' : 'sky'}>{db.level_label}</Badge>
                </li>
              ))}
            </ul>
          )}
        </section>

        {/* Groups */}
        <section className="card p-5">
          <div className="mb-4 flex items-center justify-between">
            <h2 className="text-base font-semibold text-slate-900">Group Kamu</h2>
            {leaderGroups.length > 0 && (
              <Link
                to="/approvals"
                className="inline-flex items-center gap-1 text-xs font-medium text-indigo-600 hover:text-indigo-700"
              >
                Buka Review Inbox
                <ArrowRight className="h-3.5 w-3.5" />
              </Link>
            )}
          </div>
          {groups.length === 0 ? (
            <p className="text-sm text-slate-500">
              Kamu belum tergabung dalam group apa pun. Hubungi Platform Admin untuk penugasan group.
            </p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {groups.map((g) => (
                <li key={g.group_id} className="flex items-center justify-between gap-3 py-3">
                  <div className="min-w-0">
                    <p className="truncate text-sm font-medium text-slate-900">{g.group_name}</p>
                    {g.description && (
                      <p className="truncate text-xs text-slate-500">{g.description}</p>
                    )}
                  </div>
                  {g.is_leader ? (
                    <Badge variant="amber">
                      <Crown className="h-3 w-3" />
                      Leader
                    </Badge>
                  ) : (
                    <Badge variant="neutral">Anggota</Badge>
                  )}
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      {/* Quick module links */}
      <section className="mt-6">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wide text-slate-500">
          Navigasi Cepat
        </h2>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Link to="/databases" className="card flex items-center gap-3 p-4 transition hover:border-indigo-300">
            <Database className="h-5 w-5 text-indigo-600" />
            <div>
              <p className="text-sm font-semibold text-slate-900">Database Viewer</p>
              <p className="text-xs text-slate-500">Skema, kolom, constraint & data</p>
            </div>
          </Link>
          <Link to="/migrations" className="card flex items-center gap-3 p-4 transition hover:border-indigo-300">
            <FileCode2 className="h-5 w-5 text-indigo-600" />
            <div>
              <p className="text-sm font-semibold text-slate-900">Migrations & History</p>
              <p className="text-xs text-slate-500">Riwayat & integrasi modul Orang 3</p>
            </div>
          </Link>
          <Link to="/approvals" className="card flex items-center gap-3 p-4 transition hover:border-indigo-300">
            <CheckSquare className="h-5 w-5 text-amber-600" />
            <div>
              <p className="text-sm font-semibold text-slate-900">Review Inbox</p>
              <p className="text-xs text-slate-500">Persetujuan leader lintas group</p>
            </div>
          </Link>
          <Link to="/jobs" className="card flex items-center gap-3 p-4 transition hover:border-indigo-300">
            <Activity className="h-5 w-5 text-emerald-600" />
            <div>
              <p className="text-sm font-semibold text-slate-900">Job Monitoring</p>
              <p className="text-xs text-slate-500">Antrean & worker modul Orang 4</p>
            </div>
          </Link>
        </div>

        {user?.is_platform_admin && (
          <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Link to="/admin/connections" className="card flex items-center gap-3 p-4 transition hover:border-indigo-300">
              <Server className="h-5 w-5 text-slate-700" />
              <div>
                <p className="text-sm font-semibold text-slate-900">DB Connections</p>
                <p className="text-xs text-slate-500">Koneksi PostgreSQL & pool stats</p>
              </div>
            </Link>
            <Link to="/admin/groups" className="card flex items-center gap-3 p-4 transition hover:border-indigo-300">
              <Crown className="h-5 w-5 text-amber-600" />
              <div>
                <p className="text-sm font-semibold text-slate-900">Groups & Leaders</p>
                <p className="text-xs text-slate-500">Atur anggota & leader group</p>
              </div>
            </Link>
            <Link to="/admin/access" className="card flex items-center gap-3 p-4 transition hover:border-indigo-300">
              <KeyRound className="h-5 w-5 text-slate-700" />
              <div>
                <p className="text-sm font-semibold text-slate-900">Access Matrix</p>
                <p className="text-xs text-slate-500">View Only vs View + Edit</p>
              </div>
            </Link>
            <Link to="/audit-logs" className="card flex items-center gap-3 p-4 transition hover:border-indigo-300">
              <ScrollText className="h-5 w-5 text-slate-700" />
              <div>
                <p className="text-sm font-semibold text-slate-900">Audit Logs</p>
                <p className="text-xs text-slate-500">Jejak aktivitas platform</p>
              </div>
            </Link>
          </div>
        )}
      </section>
    </div>
  );
}
