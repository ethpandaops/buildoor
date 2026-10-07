import { useCallback } from 'react';
import { useAuthContext } from '../context/AuthContext';

// postSettings applies path-based global settings (canonical registry keys,
// e.g. { 'local_build.enabled': true }) through the generic settings endpoint
// and returns the server's error message, or null on success.
export function useSettings() {
  const { isLoggedIn, getAuthHeader } = useAuthContext();

  const postSettings = useCallback(async (settings: Record<string, unknown>): Promise<string | null> => {
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = await getAuthHeader();
    if (authToken) headers['Authorization'] = `Bearer ${authToken}`;

    try {
      const response = await fetch('/api/config/settings', {
        method: 'POST',
        headers,
        body: JSON.stringify(settings),
      });
      const result = await response.json();
      if (!response.ok || result.error) {
        return result.error || response.statusText;
      }
      return null;
    } catch (err) {
      return err instanceof Error ? err.message : String(err);
    }
  }, [getAuthHeader]);

  return { isLoggedIn, postSettings };
}
