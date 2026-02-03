import { useEffect, useState } from 'react';
import type { ValidatorRegistration, ValidatorsResponse } from '../types';
import { useAuth } from './useAuth';

export function useValidators() {
  const [validators, setValidators] = useState<ValidatorRegistration[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { getAuthHeader } = useAuth();

  useEffect(() => {
    const fetchValidators = async () => {
      try {
        setLoading(true);
        setError(null);
        const headers: HeadersInit = {};
        const token = getAuthHeader();
        if (token) {
          headers['Authorization'] = `Bearer ${token}`;
        }
        const response = await fetch('/api/buildoor/validators', {
          headers,
        });

        if (!response.ok) {
          throw new Error(`Failed to fetch validators: ${response.statusText}`);
        }

        const data: ValidatorsResponse = await response.json();
        setValidators(data.validators || []);
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Unknown error');
        setValidators([]);
      } finally {
        setLoading(false);
      }
    };

    fetchValidators();
    // Refresh every 10 seconds
    const interval = setInterval(fetchValidators, 10000);
    return () => clearInterval(interval);
  }, [getAuthHeader]);

  return { validators, loading, error };
}
