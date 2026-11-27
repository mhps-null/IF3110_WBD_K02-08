import React, { createContext, useCallback, useContext, useEffect, useState } from 'react';
import { authApi } from '../api/endpoints';
import { ApiError, onUnauthorized } from '../api/client';
import type { AccessibleDatabase, Principal, UserGroupMembership } from '../types/api';

interface AuthContextValue {
  user: Principal | null;
  groups: UserGroupMembership[];
  accessibleDatabases: AccessibleDatabase[];
  loading: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refreshSession: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<Principal | null>(null);
  const [groups, setGroups] = useState<UserGroupMembership[]>([]);
  const [accessibleDatabases, setAccessibleDatabases] = useState<AccessibleDatabase[]>([]);
  const [loading, setLoading] = useState(true);

  const clearState = useCallback(() => {
    setUser(null);
    setGroups([]);
    setAccessibleDatabases([]);
  }, []);

  const refreshSession = useCallback(async () => {
    try {
      const data = await authApi.me();
      setUser(data.user);
      setGroups(data.groups ?? []);
      setAccessibleDatabases(data.accessible_databases ?? []);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        clearState();
      }
    } finally {
      setLoading(false);
    }
  }, [clearState]);

  useEffect(() => {
    onUnauthorized(() => {
      clearState();
    });
    void refreshSession();
    return () => onUnauthorized(null);
  }, [clearState, refreshSession]);

  const login = useCallback(
    async (username: string, password: string) => {
      await authApi.login(username, password);
      await refreshSession();
    },
    [refreshSession],
  );

  const logout = useCallback(async () => {
    try {
      await authApi.logout();
    } finally {
      clearState();
    }
  }, [clearState]);

  return (
    <AuthContext.Provider
      value={{ user, groups, accessibleDatabases, loading, login, logout, refreshSession }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth must be used within AuthProvider');
  }
  return ctx;
}
