import { useCallback, useEffect, useRef, useState } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type {
  BuilderKeyState,
  BuilderKeysAggregate,
  BuilderKeysResponse,
  BuilderKeysSettings,
} from '../types';

const EMPTY_AGGREGATE: BuilderKeysAggregate = {
  target: 0,
  managed: 0,
  unused: 0,
  depositing: 0,
  pending: 0,
  active: 0,
  exiting: 0,
  exited: 0,
  withdrawn: 0,
  total_balance_gwei: 0,
  total_pending_payments_gwei: 0,
  total_effective_gwei: 0,
};

const EMPTY_SETTINGS: BuilderKeysSettings = {
  target_count: 0,
  max_index: 0,
  auto_deposit: false,
  auto_exit: false,
};

/**
 * Loads the managed builder key set. The SSE `builder_keys` event carries the
 * whole set on every change, so callers pass a change counter (e.g. the stream's
 * key generation) to refetch settings alongside it.
 */
export function useBuilderKeys(changeToken?: unknown) {
  const [keys, setKeys] = useState<BuilderKeyState[]>([]);
  const [aggregate, setAggregate] = useState<BuilderKeysAggregate>(EMPTY_AGGREGATE);
  const [settings, setSettings] = useState<BuilderKeysSettings>(EMPTY_SETTINGS);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const initialFetchDone = useRef(false);

  const fetchKeys = useCallback(async () => {
    try {
      const response = await fetch('/api/buildoor/builder-keys');

      if (response.status === 404) {
        setKeys([]);
        setError('builder key registry not available');
        return;
      }

      if (!response.ok) {
        throw new Error(`Failed to fetch builder keys: ${response.statusText}`);
      }

      const data: BuilderKeysResponse = await response.json();
      setKeys(data.keys || []);
      setAggregate(data.aggregate ?? EMPTY_AGGREGATE);
      setSettings(data.settings ?? EMPTY_SETTINGS);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error');
    } finally {
      if (!initialFetchDone.current) {
        initialFetchDone.current = true;
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    fetchKeys();
  }, [fetchKeys, changeToken]);

  return { keys, aggregate, settings, loading, error, refetch: fetchKeys };
}

/** Result of a mutating builder key request. */
export interface KeyActionResult {
  ok: boolean;
  error?: string;
}

/**
 * Provides the mutating builder key actions (target, deposit, top-up, exit).
 * Every call carries the auth header the rest of the UI uses.
 */
export function useBuilderKeyActions() {
  const { isLoggedIn, getAuthHeader } = useAuthContext();

  const post = useCallback(
    async (path: string, body?: unknown): Promise<KeyActionResult> => {
      if (!isLoggedIn) {
        return { ok: false, error: 'not logged in' };
      }

      const headers: HeadersInit = { 'Content-Type': 'application/json' };
      const authToken = await getAuthHeader();
      if (authToken) {
        headers['Authorization'] = `Bearer ${authToken}`;
      }

      try {
        const response = await fetch(path, {
          method: 'POST',
          headers,
          body: body === undefined ? undefined : JSON.stringify(body),
        });

        if (!response.ok) {
          const payload = await response.json().catch(() => null);
          return { ok: false, error: payload?.error || `HTTP ${response.status}` };
        }

        return { ok: true };
      } catch (err) {
        return { ok: false, error: err instanceof Error ? err.message : 'Unknown error' };
      }
    },
    [isLoggedIn, getAuthHeader],
  );

  return {
    isLoggedIn,
    setTarget: (target: number) => post('/api/buildoor/builder-keys/target', { target }),
    depositKey: (keyIndex: number) => post(`/api/buildoor/builder-keys/${keyIndex}/deposit`),
    topupKey: (keyIndex: number) => post(`/api/buildoor/builder-keys/${keyIndex}/topup`, {}),
    exitKey: (keyIndex: number, lowerTarget: boolean) =>
      post(`/api/buildoor/builder-keys/${keyIndex}/exit`, { lower_target: lowerTarget }),
  };
}
