import { useCallback, useEffect, useState } from 'react';
import type { TxPoolPreviewResponse, TxPoolResponse } from '../types';
import { useAuth } from './useAuth';

interface TxPoolQuery {
  offset: number;
  limit: number;
  sender: string;
  sort: string;
}

// Fetches a page of the transaction pool and, on demand, the dry-run
// selection preview. refreshKey (the SSE pool version) triggers a refetch.
export function useTxPool(query: TxPoolQuery, refreshKey?: unknown) {
  const [data, setData] = useState<TxPoolResponse | null>(null);
  const [preview, setPreview] = useState<TxPoolPreviewResponse | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { getAuthHeader } = useAuth();

  const fetchPool = useCallback(async () => {
    const params = new URLSearchParams({
      offset: String(query.offset),
      limit: String(query.limit),
    });
    if (query.sender) params.set('sender', query.sender);
    if (query.sort) params.set('sort', query.sort);

    try {
      const response = await fetch(`/api/buildoor/txpool?${params.toString()}`);
      if (response.status === 404) {
        setData(null);
        setError('Transaction pool not configured (no --el-rpc)');
        return;
      }
      if (!response.ok) {
        throw new Error(`Failed to fetch pool: ${response.statusText}`);
      }
      setData(await response.json());
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error');
    } finally {
      setLoading(false);
    }
  }, [query.offset, query.limit, query.sender, query.sort]);

  const fetchPreview = useCallback(async () => {
    try {
      const response = await fetch('/api/buildoor/txpool/preview');
      const body = await response.json();
      if (!response.ok) {
        setPreview(null);
        setPreviewError(body.error || response.statusText);
        return;
      }
      setPreview(body);
      setPreviewError(null);
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : 'Unknown error');
    }
  }, []);

  useEffect(() => {
    fetchPool();
  }, [fetchPool, refreshKey]);

  const authHeaders = useCallback(async (): Promise<HeadersInit> => {
    const headers: HeadersInit = {};
    const token = await getAuthHeader();
    if (token) headers['Authorization'] = `Bearer ${token}`;
    return headers;
  }, [getAuthHeader]);

  const clear = useCallback(async (): Promise<string | null> => {
    const response = await fetch('/api/buildoor/txpool', { method: 'DELETE', headers: await authHeaders() });
    const body = await response.json();
    if (!response.ok) return body.error || response.statusText;
    await fetchPool();
    return null;
  }, [authHeaders, fetchPool]);

  const drop = useCallback(async (hash: string): Promise<string | null> => {
    const response = await fetch(`/api/buildoor/txpool/${hash}`, { method: 'DELETE', headers: await authHeaders() });
    const body = await response.json();
    if (!response.ok) return body.error || response.statusText;
    await fetchPool();
    return null;
  }, [authHeaders, fetchPool]);

  return { data, loading, error, refresh: fetchPool, preview, previewError, fetchPreview, clear, drop };
}
