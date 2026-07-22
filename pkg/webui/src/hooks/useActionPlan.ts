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
import {
  onStreamEvent,
  getConnectionGeneration,
  subscribeConnectionGeneration,
} from './useEventStream';

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
  results: Record<number, SlotResult>;
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
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Current range for the SSE patch handlers (no resubscribe on range change).
  const rangeRef = useRef({ min: minSlot, max: maxSlot });
  rangeRef.current = { min: minSlot, max: maxSlot };

  // Merge an authoritative {slots, plans} change set (POST response or SSE
  // event) into the in-range plan state; a null plan deletes the slot's entry.
  const mergePlanChange = useCallback((slots: number[], changed: (SlotPlan | null)[]) => {
    setPlans((prev) => {
      let next: Record<number, SlotPlan> | null = null;
      for (let i = 0; i < slots.length; i++) {
        const slot = toSlotNumber(slots[i]);
        if (slot < rangeRef.current.min || slot > rangeRef.current.max) continue;
        const plan = changed[i] ?? null;
        if (plan === null) {
          if (prev[slot] === undefined && next === null) continue;
          next = next ?? { ...prev };
          delete next[slot];
        } else {
          next = next ?? { ...prev };
          next[slot] = plan;
        }
      }
      return next ?? prev;
    });
  }, []);

  const fetchAll = useCallback(async () => {
    if (maxSlot < minSlot) return;

    setLoading(true);
    setError(null);

    try {
      const query = `min_slot=${minSlot}&max_slot=${maxSlot}`;
      const [planResp, resultResp] = await Promise.all([
        fetch(`/api/buildoor/action-plan?${query}`),
        fetch(`/api/buildoor/slot-results?${query}`),
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
  }, [minSlot, maxSlot]);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  // Live updates through the shared SSE connection.
  useEffect(() => {
    const offPlan = onStreamEvent('action_plan_updated', (data) => {
      const change = data as { slots?: number[]; plans?: (SlotPlan | null)[] };
      if (!change?.slots?.length) return;
      mergePlanChange(change.slots, change.plans || []);
    });

    const offResult = onStreamEvent('slot_result_updated', (data) => {
      const result = data as SlotResult;
      const slot = toSlotNumber(result?.slot);
      if (slot < 0) return;
      if (slot < rangeRef.current.min || slot > rangeRef.current.max) return;
      setResults((prev) => ({ ...prev, [slot]: result }));
    });

    return () => {
      offPlan();
      offResult();
    };
  }, [mergePlanChange]);

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
        const response = await fetch('/api/buildoor/action-plan', {
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
          mergePlanChange(change.slots, change.plans || []);
        }

        return { ok: true };
      } catch (err) {
        return { ok: false, error: err instanceof Error ? err.message : 'Unknown error' };
      }
    },
    [mergePlanChange]
  );

  return { plans, results, loading, error, refetch: fetchAll, applyUpdates };
}
