import { useState, useEffect, useCallback, useRef, useSyncExternalStore } from 'react';
import type {
  ActionPlanResponse,
  PlanUpdate,
  SlotPlan,
  SlotResult,
  SlotResultsResponse,
  UpdateActionPlanResponse,
} from '../types';
import { authStore } from '../stores/authStore';
import { authHeaders, planRedacted, useLoggedIn } from './authFetch';
import {
  onStreamEvent,
  getConnectionGeneration,
  subscribeConnectionGeneration,
} from './useEventStream';
import { apiPath } from '../utils/api';

// The backend marshals slot numbers (phase0.Slot) as JSON strings; normalize
// to a real number at every ingest point so range checks and typeof guards
// behave.
function toSlotNumber(value: unknown): number {
  const slot = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(slot) ? slot : -1;
}

export interface ApplyUpdatesResult {
  ok: boolean;
  error?: string;
  /** True when the backend rejected the update with 409 (slot past/frozen). */
  conflict?: boolean;
}

interface UseActionPlanResult {
  plans: Record<number, SlotPlan>;
  // Compact per-slot records for the grid (view=summary).
  results: Record<number, SlotResult>;
  // Full records of the slots opened in detail (loadDetail), kept live.
  details: Record<number, SlotResult>;
  loadDetail: (slot: number) => void;
  loading: boolean;
  error: string | null;
  refetch: () => void;
  applyUpdates: (updates: PlanUpdate[]) => Promise<ApplyUpdatesResult>;
}

/**
 * Fetches per-slot action plans and slot results for the inclusive slot range
 * and keeps them live via the shared SSE stream (action_plan_updated /
 * slot_result_updated), refetching after SSE reconnects.
 */
export function useActionPlan(minSlot: number, maxSlot: number): UseActionPlanResult {
  const [plans, setPlans] = useState<Record<number, SlotPlan>>({});
  const [results, setResults] = useState<Record<number, SlotResult>>({});
  const [details, setDetails] = useState<Record<number, SlotResult>>({});
  const detailSlotsRef = useRef(new Set<number>());
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Current range for the SSE patch handlers (no resubscribe on range change).
  const rangeRef = useRef({ min: minSlot, max: maxSlot });
  rangeRef.current = { min: minSlot, max: maxSlot };

  // Reads with a login carry the explicit local build transactions; without
  // one (and in every SSE event) they are redacted to a count.
  const loggedIn = useLoggedIn();
  const loggedInRef = useRef(loggedIn);
  loggedInRef.current = loggedIn;

  const fetchAll = useCallback(async () => {
    if (maxSlot < minSlot) return;

    setLoading(true);
    setError(null);

    try {
      const query = `min_slot=${minSlot}&max_slot=${maxSlot}`;
      const headers = await authHeaders();
      const [planResp, resultResp] = await Promise.all([
        fetch(apiPath(`/action-plan?${query}`), { headers }),
        fetch(apiPath(`/slot-results?${query}&view=summary`), { headers }),
      ]);

      if (!planResp.ok) {
        throw new Error(`Failed to fetch action plans: ${planResp.statusText}`);
      }
      if (!resultResp.ok) {
        throw new Error(`Failed to fetch slot results: ${resultResp.statusText}`);
      }

      const planData: ActionPlanResponse = await planResp.json();
      const resultData: SlotResultsResponse = await resultResp.json();

      const planMap: Record<number, SlotPlan> = {};
      for (const plan of planData.plans || []) {
        planMap[toSlotNumber(plan.slot)] = plan;
      }

      const resultMap: Record<number, SlotResult> = {};
      for (const result of resultData.results || []) {
        resultMap[toSlotNumber(result.slot)] = result;
      }

      setPlans(planMap);
      setResults(resultMap);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error');
      setPlans({});
      setResults({});
    } finally {
      setLoading(false);
    }
    // loggedIn: a login (or the auth client settling) unlocks redacted data.
  }, [minSlot, maxSlot, loggedIn]);

  // Stable handle for the SSE handlers, which must not resubscribe whenever
  // the visible range changes.
  const fetchAllRef = useRef(fetchAll);
  fetchAllRef.current = fetchAll;

  // Apply an authoritative {slots, plans} change set (POST response or SSE
  // event) to the in-range plan state. A stored plan always wins over a
  // recurring rule, so setting one can be merged in place — but DELETING one
  // hands the slot back to whatever rule matches it, and only the server knows
  // that. Any in-range deletion therefore refetches instead of merging.
  const applyPlanChange = useCallback((slots: number[], changed: (SlotPlan | null)[]) => {
    const { min, max } = rangeRef.current;
    const inRange: number[] = [];

    for (let i = 0; i < slots.length; i++) {
      const slot = toSlotNumber(slots[i]);
      if (slot < min || slot > max) continue;

      if ((changed[i] ?? null) === null) {
        fetchAllRef.current();
        return;
      }

      inRange.push(i);
    }

    if (inRange.length === 0) return;

    setPlans((prev) => {
      const next = { ...prev };
      for (const i of inRange) {
        next[toSlotNumber(slots[i])] = changed[i] as SlotPlan;
      }
      return next;
    });
  }, []);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  // Slots whose result is being refetched, and those invalidated again while
  // their fetch was in flight (refetched once more when it completes).
  const resultRefetchRef = useRef(new Set<number>());
  const resultStaleRef = useRef(new Set<number>());

  const refetchResult = useCallback(async (slot: number) => {
    if (resultRefetchRef.current.has(slot)) {
      resultStaleRef.current.add(slot);
      return;
    }
    resultRefetchRef.current.add(slot);
    try {
      // An opened slot needs its full record; the grid needs the summary,
      // which the full record covers too.
      const full = detailSlotsRef.current.has(slot);
      const response = await fetch(
        apiPath(`/slot-results?min_slot=${slot}&max_slot=${slot}${full ? '' : '&view=summary'}`),
        { headers: await authHeaders() },
      );
      if (!response.ok) return;
      const data: SlotResultsResponse = await response.json();
      const fresh = (data.results || []).find((r) => toSlotNumber(r.slot) === slot);
      if (!fresh) return;
      setResults((prev) => ({ ...prev, [slot]: fresh }));
      if (full) setDetails((prev) => ({ ...prev, [slot]: fresh }));
    } catch {
      // The held record stays; the next range fetch heals it.
    } finally {
      resultRefetchRef.current.delete(slot);
      if (resultStaleRef.current.delete(slot)) {
        void refetchResultRef.current(slot);
      }
    }
  }, []);
  const refetchResultRef = useRef(refetchResult);
  refetchResultRef.current = refetchResult;

  // Opening a slot loads its full record; it then follows the live updates
  // like the grid's summaries.
  const loadDetail = useCallback((slot: number) => {
    detailSlotsRef.current.add(slot);
    void refetchResultRef.current(slot);
  }, []);

  // Live updates through the shared SSE connection.
  useEffect(() => {
    const offPlan = onStreamEvent('action_plan_updated', (data) => {
      const change = data as { slots?: number[]; plans?: (SlotPlan | null)[] };
      if (!change?.slots?.length) return;
      // SSE bodies are always redacted; a logged-in reader refetches instead of
      // adopting a plan whose explicit transactions were hidden.
      if (loggedInRef.current && (change.plans || []).some((p) => planRedacted(p))) {
        fetchAllRef.current();
        return;
      }
      applyPlanChange(change.slots, change.plans || []);
    });

    // Rule changes rewrite the effective plan of every uncovered slot, so the
    // whole visible range has to come back from the authoritative endpoint.
    const offRules = onStreamEvent('action_plan_rules_updated', () => {
      fetchAllRef.current();
    });

    // The stream only names the slot whose record changed; the record itself
    // comes from REST (with the login, so explicit transactions stay intact).
    const offResult = onStreamEvent('slot_result_updated', (data) => {
      const slot = toSlotNumber((data as { slot?: number })?.slot);
      if (slot < 0) return;
      if (slot < rangeRef.current.min || slot > rangeRef.current.max) return;
      void refetchResult(slot);
    });

    return () => {
      offPlan();
      offRules();
      offResult();
    };
  }, [applyPlanChange, refetchResult]);

  // Refetch the visible range after an SSE reconnect (updates during the gap
  // were never delivered).
  const generation = useSyncExternalStore(subscribeConnectionGeneration, getConnectionGeneration);
  const lastGenerationRef = useRef(generation);
  useEffect(() => {
    if (generation !== lastGenerationRef.current) {
      lastGenerationRef.current = generation;
      fetchAll();
    }
  }, [generation, fetchAll]);

  const applyUpdates = useCallback(
    async (updates: PlanUpdate[]): Promise<ApplyUpdatesResult> => {
      const headers: HeadersInit = { 'Content-Type': 'application/json' };
      const authToken = await authStore.getAuthHeader();
      if (authToken) {
        headers['Authorization'] = `Bearer ${authToken}`;
      }

      try {
        const response = await fetch(apiPath('/action-plan'), {
          method: 'POST',
          headers,
          body: JSON.stringify({ updates }),
        });

        const body = await response.json().catch(() => null);

        if (!response.ok) {
          const message =
            (body as { error?: string } | null)?.error || `Request failed: ${response.statusText}`;
          return { ok: false, error: message, conflict: response.status === 409 };
        }

        // Merge the authoritative normalized result — never reconstruct plans
        // from our own patch.
        const change = body as UpdateActionPlanResponse;
        if (change?.slots?.length) {
          applyPlanChange(change.slots, change.plans || []);
        }

        return { ok: true };
      } catch (err) {
        return { ok: false, error: err instanceof Error ? err.message : 'Unknown error' };
      }
    },
    [applyPlanChange]
  );

  return { plans, results, details, loadDetail, loading, error, refetch: fetchAll, applyUpdates };
}
