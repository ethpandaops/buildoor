import { useCallback, useEffect, useRef, useState } from 'react';
import type { LocalBuildAvailability, LocalBuildStatus } from '../types';
import { useAuth } from './useAuth';
import { REFRESH_INTERVAL_SLOW_MS } from './refreshIntervals';

// Fetches the local build (testing_buildBlockV1) status: the probed EL
// availability, the effective settings and the pool's state. Live changes
// arrive via the service_status SSE event (refreshKey), so the background
// poll stays slow. probe() forces a fresh EL probe (authenticated).
export function useLocalBuildStatus(refreshKey?: unknown) {
  const [status, setStatus] = useState<LocalBuildStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { getAuthHeader } = useAuth();
  const initialFetchDone = useRef(false);

  const fetchStatus = useCallback(async () => {
    try {
      const response = await fetch('/api/buildoor/local-build/status');
      if (!response.ok) {
        throw new Error(`Failed to fetch local build status: ${response.statusText}`);
      }
      const data: LocalBuildStatus = await response.json();
      setStatus(data);
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
    fetchStatus();
    const interval = setInterval(fetchStatus, REFRESH_INTERVAL_SLOW_MS);
    return () => clearInterval(interval);
  }, [fetchStatus, refreshKey]);

  const probe = useCallback(async (): Promise<LocalBuildAvailability | null> => {
    const headers: HeadersInit = {};
    const token = await getAuthHeader();
    if (token) headers['Authorization'] = `Bearer ${token}`;

    const response = await fetch('/api/buildoor/local-build/probe', { method: 'POST', headers });
    const data = await response.json();
    if (!response.ok) {
      throw new Error(data.error || response.statusText);
    }
    await fetchStatus();
    return data as LocalBuildAvailability;
  }, [getAuthHeader, fetchStatus]);

  return { status, loading, error, refresh: fetchStatus, probe };
}
