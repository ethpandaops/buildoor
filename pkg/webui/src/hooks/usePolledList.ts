import { useCallback, useEffect, useRef, useState } from 'react';
import { authHeaders, useLoggedIn } from './authFetch';

export interface PolledListOptions<T, R> {
  url: string;
  intervalMs: number;
  // Extracts the rows from the response body.
  select: (body: R) => T[] | null | undefined;
  // Order applied to every fetched list (the backends return store order).
  compare?: (a: T, b: T) => number;
  // Sends the login Authorization header and refetches when the login state
  // changes.
  withAuth?: boolean;
  // Label used in error messages ("Failed to fetch <what>: ...").
  what: string;
}

export interface PolledList<T> {
  items: T[];
  // True until the first fetch settled (successfully or not).
  loading: boolean;
  // Last fetch failure; items keep the last good list meanwhile.
  error: string | null;
  // The endpoint answered 404: the backing service is not running.
  notEnabled: boolean;
  // Time of the last successful fetch (ms epoch), null before the first one.
  updatedAt: number | null;
  intervalMs: number;
  refetch: () => void;
}

// usePolledList fetches a REST list on mount and every intervalMs. A 404 is
// the backends' "service not enabled" answer and reported as notEnabled, not
// as an error.
export function usePolledList<T, R>(options: PolledListOptions<T, R>): PolledList<T> {
  const { url, intervalMs, withAuth, what } = options;
  const [items, setItems] = useState<T[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notEnabled, setNotEnabled] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const loggedIn = useLoggedIn();

  // select/compare are usually inline closures; keep the latest without
  // restarting the poll loop on every render.
  const selectRef = useRef(options.select);
  const compareRef = useRef(options.compare);
  selectRef.current = options.select;
  compareRef.current = options.compare;

  const fetchList = useCallback(async () => {
    try {
      const headers = withAuth ? await authHeaders() : {};
      const response = await fetch(url, { headers });
      if (response.status === 404) {
        setItems([]);
        setNotEnabled(true);
        setError(null);
        return;
      }
      if (!response.ok) {
        throw new Error(`Failed to fetch ${what}: ${response.status} ${response.statusText}`.trim());
      }
      const body: R = await response.json();
      const list = (selectRef.current(body) ?? []).slice();
      if (compareRef.current) list.sort(compareRef.current);
      setItems(list);
      setNotEnabled(false);
      setError(null);
      setUpdatedAt(Date.now());
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to fetch ${what}`);
    } finally {
      setLoading(false);
    }
  }, [url, withAuth, what]);

  // Authenticated lists refetch once a login lands or ends.
  const authState = withAuth ? loggedIn : false;

  useEffect(() => {
    fetchList();
    const interval = setInterval(fetchList, intervalMs);
    return () => clearInterval(interval);
  }, [fetchList, intervalMs, authState]);

  return { items, loading, error, notEnabled, updatedAt, intervalMs, refetch: fetchList };
}
