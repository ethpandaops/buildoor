import { useState, useEffect } from 'react';
import { BidWonEntry, BidsWonResponse } from '../types';
import { authStore } from '../stores/authStore';

export function useBidsWon(offset: number, limit: number) {
  const [bidsWon, setBidsWon] = useState<BidWonEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchBidsWon = async () => {
    setLoading(true);
    setError(null);

    try {
      const headers: HeadersInit = { 'Content-Type': 'application/json' };
      const authHeader = authStore.getAuthHeader();
      if (authHeader) {
        headers['Authorization'] = authHeader;
      }

      const response = await fetch(
        `/api/buildoor/bids-won?offset=${offset}&limit=${limit}`,
        { headers }
      );

      if (!response.ok) {
        throw new Error(`Failed to fetch: ${response.statusText}`);
      }

      const data: BidsWonResponse = await response.json();
      setBidsWon(data.bids_won || []);
      setTotal(data.total || 0);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error');
      setBidsWon([]);
      setTotal(0);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchBidsWon();
  }, [offset, limit]);

  return { bidsWon, total, loading, error, refetch: fetchBidsWon };
}
