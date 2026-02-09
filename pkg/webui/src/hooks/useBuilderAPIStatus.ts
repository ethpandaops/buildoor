import { useEffect, useRef, useState } from 'react';
import type { BuilderAPIStatus } from '../types';
import { useAuth } from './useAuth';

export function useBuilderAPIStatus() {
  const [status, setStatus] = useState<BuilderAPIStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { getAuthHeader } = useAuth();
  const initialFetchDone = useRef(false);

  useEffect(() => {
    const fetchStatus = async () => {
      try {
        const headers: HeadersInit = {};
        const token = getAuthHeader();
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
    // Refresh every 10 seconds
    const interval = setInterval(fetchStatus, 10000);
    return () => clearInterval(interval);
  }, [getAuthHeader]);

  return { status, loading, error };
}
