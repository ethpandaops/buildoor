import { useCallback } from 'react';
import { useAuthContext } from '../context/AuthContext';

export interface SettingsResult {
  ok: boolean;
  error?: string;
}

/**
 * Writes global settings through the generic path-based endpoint, keyed by the
 * canonical settings keys of the backend registry (e.g. `deposit_amount`,
 * `reveal.gate_mode`). The write is atomic and the backend validates every
 * value, so a rejected setting comes back as an error instead of being stored
 * and ignored. The resulting config arrives through the SSE `config` event.
 */
export function useSettings() {
  const { isLoggedIn, getAuthHeader } = useAuthContext();

  const postSettings = useCallback(
    async (settings: Record<string, unknown>): Promise<SettingsResult> => {
      const headers: HeadersInit = { 'Content-Type': 'application/json' };
      const authToken = await getAuthHeader();
      if (authToken) {
        headers['Authorization'] = `Bearer ${authToken}`;
      }

      try {
        const response = await fetch('/api/config/settings', {
          method: 'POST',
          headers,
          body: JSON.stringify(settings),
        });
        const result = await response.json().catch(() => null);

        if (!response.ok || result?.error) {
          return { ok: false, error: result?.error || `HTTP ${response.status}` };
        }

        return { ok: true };
      } catch (err) {
        return { ok: false, error: err instanceof Error ? err.message : String(err) };
      }
    },
    [getAuthHeader],
  );

  return { isLoggedIn, postSettings };
}
