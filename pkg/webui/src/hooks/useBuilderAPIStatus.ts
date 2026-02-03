import { useEffect, useState } from 'react';
import type { BuilderAPIStatus } from '../types';
import { useAuth } from './useAuth';

export function useBuilderAPIStatus() {
  const [status, setStatus] = useState<BuilderAPIStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { getAuthHeaders } = useAuth();

  useEffect(() => {
    const fetchStatus = async () => {
      try {
        setLoading(true);
        setError(null);
        const response = await fetch('/api/buildoor/builder-api-status', {
          headers: getAuthHeaders(),
        });

        if (!response.ok) {
          throw new Error(`Failed to fetch Builder API status: ${response.statusText}`);
        }

        const data: BuilderAPIStatus = await response.json();
        setStatus(data);
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Unknown error');
        setStatus(null);
      } finally {
        setLoading(false);
      }
    };

    fetchStatus();
    // Refresh every 10 seconds
    const interval = setInterval(fetchStatus, 10000);
    return () => clearInterval(interval);
  }, [getAuthHeaders]);

  return { status, loading, error };
}
