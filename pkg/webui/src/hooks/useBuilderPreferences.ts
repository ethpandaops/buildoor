import { useEffect, useRef, useState } from 'react';
import type { BuilderPreference, BuilderPreferencesResponse } from '../types';

export function useBuilderPreferences() {
  const [preferences, setPreferences] = useState<BuilderPreference[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const initialFetchDone = useRef(false);

  const fetchPreferences = async () => {
    try {
      const response = await fetch('/api/buildoor/builder-preferences');

      if (response.status === 404) {
        setPreferences([]);
        setError('builder API not enabled');
        return;
      }

      if (!response.ok) {
        throw new Error(`Failed to fetch builder preferences: ${response.statusText}`);
      }

      const data: BuilderPreferencesResponse = await response.json();
      const sorted = (data.preferences || [])
        .slice()
        .sort((a, b) => b.max_execution_payment - a.max_execution_payment);
      setPreferences(sorted);
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

  useEffect(() => {
    fetchPreferences();
    // Poll every 12 seconds (one slot) — proposers submit preferences ahead of their slot
    const interval = setInterval(fetchPreferences, 12000);
    return () => clearInterval(interval);
  }, []);

  return { preferences, loading, error, refetch: fetchPreferences };
}
