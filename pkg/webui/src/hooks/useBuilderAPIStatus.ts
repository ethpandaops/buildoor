import { useEffect, useRef, useState } from 'react';
import type { BuilderAPIStatus } from '../types';
import { useAuth } from './useAuth';
import { REFRESH_INTERVAL_SLOW_MS } from './refreshIntervals';

// refreshKey: optional dependency that triggers an immediate refetch when it
// changes (e.g. the SSE-delivered builder_api_enabled flag) — live changes
// come through SSE, so the background poll can stay slow.
export function useBuilderAPIStatus(refreshKey?: unknown) {
  const [status, setStatus] = useState<BuilderAPIStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { getAuthHeader } = useAuth();
  const initialFetchDone = useRef(false);

  useEffect(() => {
    const fetchStatus = async () => {
      try {
        const headers: HeadersInit = {};
        const token = await getAuthHeader();
        if (token) {
          headers['Authorization'] = `Bearer ${token}`;
        }
        const response = await fetch('/api/buildoor/builder-api-status', {
          headers,
        });

        if (!response.ok) {
          throw new Error(`Failed to fetch Builder API status: ${response.statusText}`);
        }

        const data: BuilderAPIStatus = await response.json();
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
    };

    fetchStatus();
    // Slow background refresh only — live changes arrive via SSE and the
    // refreshKey dependency.
    const interval = setInterval(fetchStatus, REFRESH_INTERVAL_SLOW_MS);
    return () => clearInterval(interval);
  }, [getAuthHeader, refreshKey]);

  return { status, loading, error };
}
