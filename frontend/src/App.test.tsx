import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { AppRoutes } from './App';
import { AuthProvider } from './context/AuthContext';
import { ToastProvider } from './context/ToastContext';

function mockFetch(responses: Record<string, { status: number; body: unknown }>) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    const match = responses[url] ?? { status: 404, body: { error: 'not found' } };
    return new Response(JSON.stringify(match.body), {
      status: match.status,
      headers: { 'Content-Type': 'application/json' },
    });
  });
}

describe('SPA shell, routing, and authorization smoke tests', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it('redirects unauthenticated users to login page', async () => {
    mockFetch({
      '/api/auth/me': { status: 401, body: { error: 'unauthenticated' } },
    });

    render(
      <ToastProvider>
        <AuthProvider>
          <MemoryRouter initialEntries={['/databases']}>
            <AppRoutes />
          </MemoryRouter>
        </AuthProvider>
      </ToastProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText('IF3110 Database Platform')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Masuk' })).toBeInTheDocument();
    });
  });

  it('shows 403 Forbidden when non-admin user accesses admin routes', async () => {
    mockFetch({
      '/api/auth/me': {
        status: 200,
        body: {
          user: { id: 2, username: 'alice', is_platform_admin: false },
          groups: [{ group_id: 1, group_name: 'Backend', description: '', is_leader: true }],
          accessible_databases: [
            { db_id: 1, name: 'test_ecommerce_pg', engine: 'postgres', level: 2, level_label: 'View + Edit' },
          ],
          csrf_token: 'csrf-123',
        },
      },
    });

    render(
      <ToastProvider>
        <AuthProvider>
          <MemoryRouter initialEntries={['/admin/users']}>
            <AppRoutes />
          </MemoryRouter>
        </AuthProvider>
      </ToastProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText('403 Forbidden')).toBeInTheDocument();
      expect(screen.getByText('Akses Ditolak')).toBeInTheDocument();
    });
  });

  it('renders dashboard with leader badge and accessible databases for authenticated user', async () => {
    mockFetch({
      '/api/auth/me': {
        status: 200,
        body: {
          user: { id: 2, username: 'alice', is_platform_admin: false },
          groups: [{ group_id: 1, group_name: 'Backend', description: 'Core API', is_leader: true }],
          accessible_databases: [
            { db_id: 1, name: 'test_ecommerce_pg', engine: 'postgres', level: 2, level_label: 'View + Edit' },
          ],
          csrf_token: 'csrf-123',
        },
      },
    });

    render(
      <ToastProvider>
        <AuthProvider>
          <MemoryRouter initialEntries={['/']}>
            <AppRoutes />
          </MemoryRouter>
        </AuthProvider>
      </ToastProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText('Selamat datang, alice')).toBeInTheDocument();
      expect(screen.getByText('test_ecommerce_pg')).toBeInTheDocument();
      expect(screen.getByText('View + Edit')).toBeInTheDocument();
      expect(screen.getByText('Leader (1)')).toBeInTheDocument();
    });
  });

  it('renders 404 page on unknown authenticated route', async () => {
    mockFetch({
      '/api/auth/me': {
        status: 200,
        body: {
          user: { id: 1, username: 'admin', is_platform_admin: true },
          groups: [],
          accessible_databases: [],
          csrf_token: 'csrf-123',
        },
      },
    });

    render(
      <ToastProvider>
        <AuthProvider>
          <MemoryRouter initialEntries={['/halaman-tidak-ada']}>
            <AppRoutes />
          </MemoryRouter>
        </AuthProvider>
      </ToastProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText('404 Not Found')).toBeInTheDocument();
    });
  });
});
