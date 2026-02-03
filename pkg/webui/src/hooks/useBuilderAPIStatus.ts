import { useState, useEffect, useCallback } from 'react';
import { authStore } from '../stores/authStore';
import type { BuilderAPIStatus } from '../types';

interface UseBuilderAPIStatusResult {
  status: BuilderAPIStatus | null;
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
}

export function useBuilderAPIStatus(): UseBuilderAPIStatusResult {
  const [status, setStatus] = useState<BuilderAPIStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchStatus = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);

      const authToken = authStore.getAuthHeader();
      const headers: HeadersInit = {
        'Content-Type': 'application/json',
      };
      if (authToken) {
        headers['Authorization'] = `Bearer ${authToken}`;
      }

      const response = await fetch('/api/buildoor/builder-api-status', { headers });
      if (!response.ok) {
        throw new Error(`Failed to fetch Builder API status: ${response.statusText}`);
      }

      const data: BuilderAPIStatus = await response.json();
      setStatus(data);
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : 'Unknown error';
      setError(errorMessage);
      console.error('Error fetching Builder API status:', err);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchStatus();
    // Refresh every 10 seconds
    const interval = setInterval(fetchStatus, 10000);
    return () => clearInterval(interval);
  }, [fetchStatus]);

  return {
    status,
    loading,
    error,
    refresh: fetchStatus,
  };
}
