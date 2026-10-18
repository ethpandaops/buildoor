import { useCallback, useEffect, useState } from 'react';
import type { ELMempoolResponse, FeeDistribution } from '../types';
import { useAuth } from './useAuth';

interface ELMempoolQuery {
  offset: number;
  limit: number;
  sender: string;
  sort: string;
}

const POLL_MS = 3000;

// Polls the EL mempool mirror page and the fee distribution while `active`
// (the tab is open and the page visible). The fee distribution includes
// buildoor's private transactions only for logged-in readers.
export function useELMempool(query: ELMempoolQuery, active: boolean) {
  const [data, setData] = useState<ELMempoolResponse | null>(null);
  const [fees, setFees] = useState<FeeDistribution | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const { getAuthHeader, isLoggedIn } = useAuth();

  const fetchAll = useCallback(async () => {
    const params = new URLSearchParams({ offset: String(query.offset), limit: String(query.limit) });
    if (query.sender) params.set('sender', query.sender);
    if (query.sort) params.set('sort', query.sort);

    const headers: HeadersInit = {};
    const token = await getAuthHeader();
    if (token) headers['Authorization'] = `Bearer ${token}`;

    try {
      const [listRes, feesRes] = await Promise.all([
        fetch(`/api/buildoor/txpool/el-mempool?${params.toString()}`),
        fetch('/api/buildoor/txpool/fees', { headers }),
      ]);
      if (listRes.status === 404) {
        setData(null);
        setError('Transaction pool not configured (no --el-rpc)');
        return;
      }
      if (!listRes.ok) throw new Error(`Failed to fetch the EL mempool: ${listRes.statusText}`);
      setData(await listRes.json());
      setFees(feesRes.ok ? await feesRes.json() : null);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
    // isLoggedIn re-runs the fetch so the private series appears on login.
  }, [query.offset, query.limit, query.sender, query.sort, getAuthHeader, isLoggedIn]);

  useEffect(() => {
    if (!active) return undefined;
    fetchAll();
    const interval = setInterval(fetchAll, POLL_MS);
    return () => clearInterval(interval);
  }, [active, fetchAll]);

  return { data, fees, error, loading };
}
