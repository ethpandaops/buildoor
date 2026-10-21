import { useEffect, useState } from 'react';
import type { NextBlockEstimates } from '../types';
import { useAuth } from './useAuth';
import { apiPath } from '../utils/api';

// useNextBlock completes the streamed estimates (next_block event, public
// aggregates) with their transactions for logged-in readers: fetched once
// per estimate set while `active`, never polled. Until a new set's
// transactions arrive, the previous ones of the same slot stay, so the list
// does not flash empty on every estimate.
export function useNextBlock(estimates: NextBlockEstimates | null, active: boolean): NextBlockEstimates | null {
  const [details, setDetails] = useState<NextBlockEstimates | null>(null);
  const { getAuthHeader, isLoggedIn } = useAuth();
  const version = estimates?.version;

  useEffect(() => {
    if (!active || !isLoggedIn || version === undefined) return undefined;

    let cancelled = false;
    (async () => {
      try {
        const headers: HeadersInit = {};
        const token = await getAuthHeader();
        if (token) headers['Authorization'] = `Bearer ${token}`;
        const response = await fetch(apiPath('/next-block'), { headers });
        if (!cancelled && response.ok) setDetails(await response.json());
      } catch {
        // the transactions are an addition: the aggregates stand alone
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [active, isLoggedIn, version, getAuthHeader]);

  if (!estimates || !details) return estimates;

  return {
    ...estimates,
    estimates: estimates.estimates.map((estimate) => {
      const detailed = details.estimates.find((d) => d.slot === estimate.slot && d.basis === estimate.basis);
      return detailed?.txs ? { ...estimate, txs: detailed.txs, txs_total: detailed.txs_total } : estimate;
    }),
  };
}
