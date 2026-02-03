import { useState, useEffect, useCallback } from 'react';
import { authStore } from '../stores/authStore';
import type { ValidatorsResponse } from '../types';

interface UseValidatorsResult {
  validators: ValidatorsResponse['validators'];
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
}

export function useValidators(): UseValidatorsResult {
  const [validators, setValidators] = useState<ValidatorsResponse['validators']>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchValidators = useCallback(async () => {
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

      const response = await fetch('/api/buildoor/validators', { headers });
      if (!response.ok) {
        throw new Error(`Failed to fetch validators: ${response.statusText}`);
      }

      const data: ValidatorsResponse = await response.json();
      setValidators(data.validators || []);
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : 'Unknown error';
      setError(errorMessage);
      console.error('Error fetching validators:', err);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchValidators();
    // Refresh every 10 seconds
    const interval = setInterval(fetchValidators, 10000);
    return () => clearInterval(interval);
  }, [fetchValidators]);

  return {
    validators,
    loading,
    error,
    refresh: fetchValidators,
  };
}
