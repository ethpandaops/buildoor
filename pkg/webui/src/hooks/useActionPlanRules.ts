import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';
import type { ActionPlanRulesResponse, SlotRule } from '../types';
import { authStore } from '../stores/authStore';
import { authHeaders, planRedacted, useLoggedIn } from './authFetch';
import {
  onStreamEvent,
  getConnectionGeneration,
  subscribeConnectionGeneration,
} from './useEventStream';

export interface SaveRulesResult {
  ok: boolean;
  error?: string;
}

interface UseActionPlanRulesResult {
  rules: SlotRule[];
  loading: boolean;
  error: string | null;
  refetch: () => void;
  /** Replaces the whole rule set atomically (the backend validates all-or-nothing). */
  saveRules: (rules: SlotRule[]) => Promise<SaveRulesResult>;
}

/**
 * Fetches the recurring action plan rules and keeps them live via the shared
 * SSE stream (action_plan_rules_updated carries the authoritative set).
 */
export function useActionPlanRules(): UseActionPlanRulesResult {
  const [rules, setRules] = useState<SlotRule[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Reads with a login carry the explicit local build transactions; without
  // one (and in every SSE event) they are redacted to a count.
  const loggedIn = useLoggedIn();
  const loggedInRef = useRef(loggedIn);
  loggedInRef.current = loggedIn;

  const fetchRules = useCallback(async () => {
    setLoading(true);
    setError(null);

    try {
      const response = await fetch('/api/buildoor/action-plan/rules', { headers: await authHeaders() });
      if (!response.ok) {
        throw new Error(`Failed to fetch action plan rules: ${response.statusText}`);
      }

      const data: ActionPlanRulesResponse = await response.json();
      setRules(data.rules || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error');
      setRules([]);
    } finally {
      setLoading(false);
    }
    // loggedIn: a login (or the auth client settling) unlocks redacted data.
  }, [loggedIn]);

  useEffect(() => {
    fetchRules();
  }, [fetchRules]);

  useEffect(
    () =>
      onStreamEvent('action_plan_rules_updated', (data) => {
        const change = data as { rules?: SlotRule[] };
        if (!change?.rules) return;
        // SSE bodies are always redacted: a logged-in reader refetches rather
        // than adopt rules whose explicit transactions were hidden (saving the
        // set back would then be refused).
        if (loggedInRef.current && change.rules.some((r) => planRedacted(r))) {
          fetchRules();
          return;
        }
        setRules(change.rules);
      }),
    [fetchRules]
  );

  // Refetch after an SSE reconnect (changes during the gap were never delivered).
  const generation = useSyncExternalStore(subscribeConnectionGeneration, getConnectionGeneration);
  const lastGenerationRef = useRef(generation);
  useEffect(() => {
    if (generation !== lastGenerationRef.current) {
      lastGenerationRef.current = generation;
      fetchRules();
    }
  }, [generation, fetchRules]);

  const saveRules = useCallback(async (next: SlotRule[]): Promise<SaveRulesResult> => {
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = await authStore.getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }

    try {
      const response = await fetch('/api/buildoor/action-plan/rules', {
        method: 'POST',
        headers,
        body: JSON.stringify({ rules: next }),
      });

      const body = await response.json().catch(() => null);

      if (!response.ok) {
        const message =
          (body as { error?: string } | null)?.error || `Request failed: ${response.statusText}`;
        return { ok: false, error: message };
      }

      // Adopt the authoritative set — never our own optimistic version.
      setRules((body as ActionPlanRulesResponse)?.rules || []);

      return { ok: true };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : 'Unknown error' };
    }
  }, []);

  return { rules, loading, error, refetch: fetchRules, saveRules };
}
