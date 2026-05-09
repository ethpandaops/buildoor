import { useEffect, useRef, useState } from 'react';
import type { ProposerPreference, ProposerPreferencesResponse } from '../types';

export function useProposerPreferences() {
  const [preferences, setPreferences] = useState<ProposerPreference[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const initialFetchDone = useRef(false);

  const fetchPreferences = async () => {
    try {
      const response = await fetch('/api/buildoor/proposer-preferences');

      if (response.status === 404) {
        setPreferences([]);
        setError('proposer preferences service not enabled');
        return;
      }

      if (!response.ok) {
        throw new Error(`Failed to fetch proposer preferences: ${response.statusText}`);
      }

      const data: ProposerPreferencesResponse = await response.json();
      const sorted = (data.preferences || []).slice().sort((a, b) => b.slot - a.slot);
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
    // Poll every 12 seconds (one slot) — preferences change slot-by-slot
    const interval = setInterval(fetchPreferences, 12000);
    return () => clearInterval(interval);
  }, []);

  return { preferences, loading, error, refetch: fetchPreferences };
}
