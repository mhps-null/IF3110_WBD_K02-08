import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { AuthProvider } from './context/AuthContext';
import { ToastProvider } from './context/ToastContext';
import { ErrorBoundary } from './components/shared/ErrorBoundary';
import { AppShell } from './components/shared/AppShell';
import { ProtectedRoute } from './components/shared/ProtectedRoute';
import { LoginPage } from './pages/LoginPage';
import { DashboardPage } from './pages/DashboardPage';
import { DatabasesPage } from './pages/DatabasesPage';
import { SchemaViewerPage } from './pages/SchemaViewerPage';
import { AdminUsersPage } from './pages/AdminUsersPage';
import { AdminGroupsPage } from './pages/AdminGroupsPage';
import { AdminGroupDetailPage } from './pages/AdminGroupDetailPage';
import { AdminConnectionsPage } from './pages/AdminConnectionsPage';
import { AdminAccessPage } from './pages/AdminAccessPage';
import { AuditLogsPage } from './pages/AuditLogsPage';
import { MigrationsShellPage } from './pages/MigrationsShellPage';
import { ApprovalsInboxPage } from './pages/ApprovalsInboxPage';
import { JobsMonitorPage } from './pages/JobsMonitorPage';
import { ForbiddenPage } from './pages/ForbiddenPage';
import { NotFoundPage } from './pages/NotFoundPage';

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/403" element={<ForbiddenPage />} />

      <Route
        element={
          <ProtectedRoute>
            <AppShell />
          </ProtectedRoute>
        }
      >
        <Route path="/" element={<DashboardPage />} />
        <Route path="/databases" element={<DatabasesPage />} />
        <Route path="/databases/:id/schema" element={<SchemaViewerPage />} />
        <Route path="/databases/:id/migrations" element={<MigrationsShellPage />} />
        <Route path="/migrations" element={<MigrationsShellPage />} />
        <Route path="/approvals" element={<ApprovalsInboxPage />} />
        <Route path="/jobs" element={<JobsMonitorPage />} />

        {/* Platform Admin routes */}
        <Route
          path="/admin/users"
          element={
            <ProtectedRoute requireAdmin>
              <AdminUsersPage />
            </ProtectedRoute>
          }
        />
        <Route
          path="/admin/groups"
          element={
            <ProtectedRoute requireAdmin>
              <AdminGroupsPage />
            </ProtectedRoute>
          }
        />
        <Route
          path="/admin/groups/:id"
          element={
            <ProtectedRoute requireAdmin>
              <AdminGroupDetailPage />
            </ProtectedRoute>
          }
        />
        <Route
          path="/admin/connections"
          element={
            <ProtectedRoute requireAdmin>
              <AdminConnectionsPage />
            </ProtectedRoute>
          }
        />
        <Route
          path="/admin/access"
          element={
            <ProtectedRoute requireAdmin>
              <AdminAccessPage />
            </ProtectedRoute>
          }
        />
        <Route
          path="/audit-logs"
          element={
            <ProtectedRoute requireAdmin>
              <AuditLogsPage />
            </ProtectedRoute>
          }
        />

        <Route path="/admin" element={<Navigate to="/admin/users" replace />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}

export default function App() {
  return (
    <ErrorBoundary>
      <ToastProvider>
        <AuthProvider>
          <BrowserRouter>
            <AppRoutes />
          </BrowserRouter>
        </AuthProvider>
      </ToastProvider>
    </ErrorBoundary>
  );
}
