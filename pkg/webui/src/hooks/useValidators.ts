import { useEffect, useRef, useState } from 'react';
import type { ValidatorRegistration, ValidatorsResponse } from '../types';
import { useAuth } from './useAuth';

export function useValidators() {
  const [validators, setValidators] = useState<ValidatorRegistration[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { getAuthHeader } = useAuth();
  const initialFetchDone = useRef(false);

  useEffect(() => {
    const fetchValidators = async () => {
      try {
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

    fetchValidators();
    // Refresh every 6.5 minutes (slightly longer than 1 epoch to avoid unnecessary requests)
    const interval = setInterval(fetchValidators, 390000);
    return () => clearInterval(interval);
  }, [getAuthHeader]);

  return { validators, loading, error };
}
