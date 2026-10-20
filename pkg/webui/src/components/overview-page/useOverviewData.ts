import { useEffect, useRef, useState } from 'react';
import { onStreamEvent } from '../../hooks/useEventStream';
import type { BidWonEntry, BidsWonResponse, SlotResult, SlotResultsResponse } from '../../types';
import { apiPath } from '../../utils/api';

// Number of recent wins shown in the Blocks won table.
export const RECENT_WINS = 8;
// Width of the "Last N slots" strip.
export const STRIP_SLOTS = 16;

export interface RecentWins {
  entries: BidWonEntry[];
  total: number | null;
  loaded: boolean;
}

// useRecentWins loads the first page of won blocks once per stream connection
// and refetches it only when the stream reports a new win. It never polls:
// the page is public and may have many concurrent viewers.
export function useRecentWins(connectionGeneration: number): RecentWins {
  const [state, setState] = useState<RecentWins>({ entries: [], total: null, loaded: false });

  useEffect(() => {
    let active = true;
    let inFlight = false;
    let again = false;

    const load = async () => {
      if (inFlight) {
        again = true;
        return;
      }
      inFlight = true;
      try {
        const res = await fetch(apiPath(`/bids-won?offset=0&limit=${RECENT_WINS}`));
        if (!res.ok) throw new Error(res.statusText);
        const data: BidsWonResponse = await res.json();
        if (active) setState({ entries: data.bids_won ?? [], total: data.total ?? 0, loaded: true });
      } catch {
        if (active) setState((prev) => ({ ...prev, loaded: true }));
      } finally {
        inFlight = false;
        if (again && active) {
          again = false;
          void load();
        }
      }
    };

    void load();
    const unsubscribe = onStreamEvent('bid_won', () => void load());
    return () => {
      active = false;
      unsubscribe();
    };
  }, [connectionGeneration]);

  return state;
}

// useStripResults fetches the slot results of the strip's range ONCE per
// stream connection (as soon as the current slot is known). Newer slots are
// derived from the live slot states by the strip itself.
export function useStripResults(
  connectionGeneration: number,
  currentSlot: number,
): Record<number, SlotResult> {
  const [results, setResults] = useState<Record<number, SlotResult>>({});
  const fetchedGeneration = useRef<number | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // The current slot is a dependency only to start the first fetch once it is
  // known; a fetch is not cancelled when the slot advances.
  useEffect(() => {
    if (currentSlot <= 0 || fetchedGeneration.current === connectionGeneration) return;
    fetchedGeneration.current = connectionGeneration;

    const minSlot = Math.max(0, currentSlot - STRIP_SLOTS + 1);
    fetch(apiPath(`/slot-results?min_slot=${minSlot}&max_slot=${currentSlot}`))
      .then((res) => (res.ok ? (res.json() as Promise<SlotResultsResponse>) : null))
      .then((data) => {
        if (!data || !mounted.current) return;
        const map: Record<number, SlotResult> = {};
        for (const result of data.results ?? []) map[result.slot] = result;
        setResults(map);
      })
      .catch(() => {
        // The strip falls back to the live slot states.
      });
  }, [connectionGeneration, currentSlot]);

  return results;
}
