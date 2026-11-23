export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

let cachedCSRFToken = '';
let unauthorizedListener: (() => void) | null = null;

export function setCachedCSRFToken(token?: string) {
  if (token) {
    cachedCSRFToken = token;
  }
}

export function clearCachedCSRFToken() {
  cachedCSRFToken = '';
}

export function onUnauthorized(listener: (() => void) | null) {
  unauthorizedListener = listener;
}

export async function ensureCSRFToken(forceRefresh = false): Promise<string> {
  if (cachedCSRFToken && !forceRefresh) {
    return cachedCSRFToken;
  }
  const res = await fetch('/api/auth/csrf', {
    method: 'GET',
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    throw new ApiError(res.status, 'Gagal memperoleh token CSRF.');
  }
  const data = (await res.json()) as { csrf_token?: string };
  cachedCSRFToken = data.csrf_token || '';
  return cachedCSRFToken;
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  body?: unknown;
  skipAuthRedirect?: boolean;
}

export async function apiRequest<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? 'GET';
  const isMutation = method !== 'GET';

  const exec = async (csrfToken: string): Promise<Response> => {
    const headers: Record<string, string> = {
      Accept: 'application/json',
    };
    if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }
    if (isMutation && csrfToken) {
      headers['X-CSRF-Token'] = csrfToken;
    }
    return fetch(path, {
      method,
      credentials: 'same-origin',
      headers,
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    });
  };

  let token = isMutation ? await ensureCSRFToken(false) : '';
  let res = await exec(token);

  // If CSRF cookie rotated or expired, refresh token once and retry.
  if (isMutation && res.status === 403) {
    const clone = res.clone();
    const errText = await clone.text();
    if (errText.toLowerCase().includes('ditolak') || errText.toLowerCase().includes('csrf')) {
      token = await ensureCSRFToken(true);
      res = await exec(token);
    }
  }

  const contentType = res.headers.get('Content-Type') || '';
  const isJSON = contentType.includes('application/json');
  const payload = isJSON ? await res.json().catch(() => ({})) : await res.text();

  if (!res.ok) {
    const message =
      typeof payload === 'object' && payload !== null && 'error' in payload
        ? String((payload as { error: unknown }).error)
        : typeof payload === 'object' && payload !== null && 'message' in payload
          ? String((payload as { message: unknown }).message)
          : typeof payload === 'string' && payload.trim()
            ? payload.trim()
            : `Request gagal (${res.status})`;

    if (res.status === 401 && !opts.skipAuthRedirect && unauthorizedListener) {
      unauthorizedListener();
    }
    throw new ApiError(res.status, message);
  }

  return payload as T;
}
