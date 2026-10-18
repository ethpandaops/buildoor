import { useCallback, useEffect, useSyncExternalStore } from 'react';
import type { LocalBuildAvailability, LocalBuildStatus } from '../types';
import { authHeaders } from './authFetch';
import { REFRESH_INTERVAL_SLOW_MS } from './refreshIntervals';

// The local build (testing_buildBlockV1) status: the probed EL availability,
// the effective settings and the pool's state. One shared poll serves every
// mounted consumer (dashboard card, settings drawer), so a page shows it
// several times without fetching it several times.

interface StatusState {
  status: LocalBuildStatus | null;
  loading: boolean;
  error: string | null;
}

let state: StatusState = { status: null, loading: true, error: null };
const listeners = new Set<() => void>();
let consumers = 0;
let timer: ReturnType<typeof setInterval> | null = null;

function emit(next: Partial<StatusState>) {
  state = { ...state, ...next };
  listeners.forEach((listener) => listener());
}

async function fetchStatus() {
  try {
    const response = await fetch('/api/buildoor/local-build/status');
    if (!response.ok) {
      throw new Error(`Failed to fetch local build status: ${response.statusText}`);
    }
    emit({ status: (await response.json()) as LocalBuildStatus, error: null, loading: false });
  } catch (err) {
    emit({ error: err instanceof Error ? err.message : 'Unknown error', loading: false });
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  consumers++;
  if (consumers === 1) {
    void fetchStatus();
    timer = setInterval(fetchStatus, REFRESH_INTERVAL_SLOW_MS);
  }
  return () => {
    listeners.delete(listener);
    consumers--;
    if (consumers === 0 && timer) {
      clearInterval(timer);
      timer = null;
    }
  };
}

function getState() {
  return state;
}

// useLocalBuildStatus returns the shared status. A change of refreshKey
// (e.g. the SSE service_status flags) refetches it at once; probe() forces a
// fresh EL probe (authenticated).
export function useLocalBuildStatus(refreshKey?: unknown) {
  const current = useSyncExternalStore(subscribe, getState);

  useEffect(() => {
    if (refreshKey !== undefined) void fetchStatus();
  }, [refreshKey]);

  const probe = useCallback(async (): Promise<LocalBuildAvailability | null> => {
    const response = await fetch('/api/buildoor/local-build/probe', {
      method: 'POST',
      headers: await authHeaders(),
    });
    const data = await response.json();
    if (!response.ok) {
      throw new Error(data.error || response.statusText);
    }
    await fetchStatus();
    return data as LocalBuildAvailability;
  }, []);

  return { ...current, refresh: fetchStatus, probe };
}
