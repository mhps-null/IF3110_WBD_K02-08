import React, { useState } from 'react';
import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import {
  Activity,
  CheckSquare,
  Crown,
  Database,
  FileCode2,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Menu,
  ScrollText,
  Server,
  Shield,
  Users,
  X,
} from 'lucide-react';
import { useAuth } from '../../context/AuthContext';
import { useToast } from '../../context/ToastContext';
import { Badge } from './UIStates';

interface NavItem {
  to: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  end?: boolean;
}

const mainNav: NavItem[] = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard, end: true },
  { to: '/databases', label: 'Databases & Viewer', icon: Database },
  { to: '/migrations', label: 'Migrations & History', icon: FileCode2 },
  { to: '/approvals', label: 'Review Inbox', icon: CheckSquare },
  { to: '/jobs', label: 'Job Monitoring', icon: Activity },
];

const adminNav: NavItem[] = [
  { to: '/admin/connections', label: 'DB Connections', icon: Server },
  { to: '/admin/users', label: 'Users', icon: Users },
  { to: '/admin/groups', label: 'Groups & Leaders', icon: Crown },
  { to: '/admin/access', label: 'Access Matrix', icon: KeyRound },
  { to: '/audit-logs', label: 'Audit Logs', icon: ScrollText },
];

export function AppShell() {
  const { user, groups, logout } = useAuth();
  const { notify } = useToast();
  const navigate = useNavigate();
  const [drawerOpen, setDrawerOpen] = useState(false);

  const leaderCount = groups.filter((g) => g.is_leader).length;

  const handleLogout = async () => {
    await logout();
    notify('success', 'Berhasil keluar dari sesi.');
    navigate('/login', { replace: true });
  };

  const renderLinks = (items: NavItem[]) =>
    items.map(({ to, label, icon: Icon, end }) => (
      <NavLink
        key={to}
        to={to}
        end={end}
        onClick={() => setDrawerOpen(false)}
        className={({ isActive }) =>
          `flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition ${
            isActive
              ? 'bg-indigo-50 text-indigo-700'
              : 'text-slate-700 hover:bg-slate-100 hover:text-slate-900'
          }`
        }
      >
        <Icon className="h-4 w-4 shrink-0" />
        <span className="truncate">{label}</span>
      </NavLink>
    ));

  const sidebarContent = (
    <div className="flex h-full flex-col">
      <div className="flex h-16 items-center gap-2.5 border-b border-slate-200 px-4">
        <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-600 text-white shadow-sm">
          <Database className="h-5 w-5" />
        </div>
        <div className="min-w-0">
          <p className="truncate text-sm font-bold text-slate-900">IF3110 DB Platform</p>
          <p className="truncate text-xs text-slate-500">Milestone 2 SPA</p>
        </div>
      </div>

      <nav className="flex-1 space-y-6 overflow-y-auto px-3 py-4" aria-label="Navigasi utama">
        <div>
          <p className="mb-1.5 px-3 text-xs font-semibold uppercase tracking-wider text-slate-400">
            Workspace
          </p>
          <div className="space-y-1">{renderLinks(mainNav)}</div>
        </div>

        {user?.is_platform_admin && (
          <div>
            <p className="mb-1.5 px-3 text-xs font-semibold uppercase tracking-wider text-slate-400">
              Platform Admin
            </p>
            <div className="space-y-1">{renderLinks(adminNav)}</div>
          </div>
        )}
      </nav>

      <div className="border-t border-slate-200 p-3">
        <div className="mb-3 rounded-lg bg-slate-50 p-3">
          <p className="truncate text-sm font-semibold text-slate-900">{user?.username}</p>
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {user?.is_platform_admin && (
              <Badge variant="indigo">
                <Shield className="h-3 w-3" />
                Platform Admin
              </Badge>
            )}
            {leaderCount > 0 && (
              <Badge variant="amber">
                <Crown className="h-3 w-3" />
                Leader ({leaderCount})
              </Badge>
            )}
            {!user?.is_platform_admin && leaderCount === 0 && (
              <Badge variant="neutral">Member</Badge>
            )}
          </div>
        </div>
        <button
          type="button"
          onClick={handleLogout}
          className="btn-secondary w-full justify-center text-xs"
        >
          <LogOut className="h-4 w-4" />
          Keluar
        </button>
      </div>
    </div>
  );

  return (
    <div className="flex min-h-screen bg-slate-50">
      {/* Desktop sidebar */}
      <aside className="hidden w-64 shrink-0 border-r border-slate-200 bg-white lg:fixed lg:inset-y-0 lg:left-0 lg:z-30 lg:block">
        <div className="h-full">{sidebarContent}</div>
      </aside>

      {/* Mobile slide-over drawer */}
      {drawerOpen && (
        <div className="fixed inset-0 z-40 flex lg:hidden" role="dialog" aria-modal="true">
          <div
            className="fixed inset-0 bg-slate-900/50"
            onClick={() => setDrawerOpen(false)}
            aria-hidden="true"
          />
          <div className="relative z-10 flex w-72 max-w-[85vw] flex-col bg-white shadow-xl">
            <button
              type="button"
              onClick={() => setDrawerOpen(false)}
              aria-label="Tutup menu"
              className="absolute right-3 top-4 rounded-lg p-1.5 text-slate-500 hover:bg-slate-100"
            >
              <X className="h-5 w-5" />
            </button>
            {sidebarContent}
          </div>
        </div>
      )}

      {/* Main viewport */}
      <div className="flex min-w-0 flex-1 flex-col lg:pl-64">
        <header className="sticky top-0 z-30 flex h-14 items-center justify-between border-b border-slate-200 bg-white px-4 lg:hidden">
          <button
            type="button"
            onClick={() => setDrawerOpen(true)}
            aria-label="Buka menu navigasi"
            className="rounded-lg p-2 text-slate-600 hover:bg-slate-100"
          >
            <Menu className="h-5 w-5" />
          </button>
          <span className="truncate text-sm font-bold text-slate-900">IF3110 DB Platform</span>
          <span className="truncate text-xs font-medium text-slate-500">{user?.username}</span>
        </header>

        <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-6 sm:px-6 lg:px-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
