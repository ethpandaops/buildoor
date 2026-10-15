import { useCallback, useEffect, useState } from 'react';
import type { CreateRPCKeyResponse, RPCAccessResponse, RPCKey } from '../types';
import { authHeaders, useLoggedIn } from './authFetch';

const POLL_INTERVAL_MS = 5000;

export interface RPCKeyUpdate {
  label?: string;
  enabled?: boolean;
  rate_limit?: number;
  default_rate_limit?: true;
}

type Outcome<T> = { ok: true; value: T } | { ok: false; error: string };

async function call<T>(url: string, method: string, body?: unknown): Promise<Outcome<T>> {
  try {
    const response = await fetch(url, {
      method,
      headers: await authHeaders(body !== undefined ? { 'Content-Type': 'application/json' } : undefined),
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const result = await response.json().catch(() => null);
    if (!response.ok) {
      return { ok: false, error: (result as { error?: string } | null)?.error || `HTTP ${response.status}` };
    }
    return { ok: true, value: result as T };
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : String(err) };
  }
}

// useRPCAccess polls the /rpc ingress access state (API keys, per-caller
// stats, read upstreams) while `active` and wraps the key mutations. The
// endpoint needs a login: `unauthorized` is set on 401, `unavailable` on 404
// (no ingress without --el-rpc).
export function useRPCAccess(active: boolean) {
  const loggedIn = useLoggedIn();
  const [data, setData] = useState<RPCAccessResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);
  const [unavailable, setUnavailable] = useState(false);

  const fetchAccess = useCallback(async () => {
    try {
      const response = await fetch('/api/buildoor/rpc-access', { headers: await authHeaders() });
      setUnauthorized(response.status === 401);
      setUnavailable(response.status === 404);
      if (response.status === 401 || response.status === 404) {
        setData(null);
        setError(null);
        return;
      }
      if (!response.ok) {
        throw new Error(`Failed to fetch RPC access: ${response.statusText}`);
      }
      setData(await response.json());
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error');
    }
    // loggedIn: a login (or the auth client settling) unlocks the endpoint.
  }, [loggedIn]);

  useEffect(() => {
    if (!active) return undefined;
    fetchAccess();
    const interval = setInterval(fetchAccess, POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [active, fetchAccess]);

  const createKey = useCallback(
    async (label: string, rateLimit?: number): Promise<Outcome<CreateRPCKeyResponse>> => {
      const result = await call<CreateRPCKeyResponse>('/api/buildoor/rpc-access/keys', 'POST', {
        label,
        ...(rateLimit !== undefined ? { rate_limit: rateLimit } : {}),
      });
      if (result.ok) fetchAccess();
      return result;
    },
    [fetchAccess]
  );

  const updateKey = useCallback(
    async (id: string, update: RPCKeyUpdate): Promise<Outcome<RPCKey>> => {
      const result = await call<RPCKey>(`/api/buildoor/rpc-access/keys/${encodeURIComponent(id)}`, 'POST', update);
      if (result.ok) fetchAccess();
      return result;
    },
    [fetchAccess]
  );

  const deleteKey = useCallback(
    async (id: string): Promise<Outcome<unknown>> => {
      const result = await call<unknown>(`/api/buildoor/rpc-access/keys/${encodeURIComponent(id)}`, 'DELETE');
      if (result.ok) fetchAccess();
      return result;
    },
    [fetchAccess]
  );

  return { data, error, unauthorized, unavailable, refresh: fetchAccess, createKey, updateKey, deleteKey };
}
