import { useEffect, useState } from 'react';
import type { InstanceStatus, OverviewHost, OverviewResponse } from './types';

const POLL_INTERVAL_MS = 5000;
const REQUEST_TIMEOUT_MS = 4500;

async function fetchOverview(host: OverviewHost, signal: AbortSignal): Promise<OverviewResponse> {
  const resp = await fetch(`/api/overview/proxy/${host.id}`, { signal });
  if (!resp.ok) {
    let detail = `HTTP ${resp.status}`;
    try {
      const body = await resp.json();
      if (body && typeof body.error === 'string') {
        detail = body.error;
      }
    } catch {
      // ignore JSON parse failures — keep the HTTP status
    }
    throw new Error(detail);
  }
  return (await resp.json()) as OverviewResponse;
}

export function useOverviewHosts() {
  const [hosts, setHosts] = useState<OverviewHost[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetch('/api/overview/hosts')
      .then((r) => r.json())
      .then((data) => {
        if (cancelled) return;
        setHosts(Array.isArray(data?.hosts) ? data.hosts : []);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { hosts, error };
}

export function useOverviewPolling(hosts: OverviewHost[] | null) {
  const [statuses, setStatuses] = useState<Record<number, InstanceStatus>>({});

  useEffect(() => {
    if (!hosts || hosts.length === 0) return;

    setStatuses((prev) => {
      const next: Record<number, InstanceStatus> = {};
      for (const host of hosts) {
        next[host.id] = prev[host.id] ?? { state: 'loading' };
      }
      return next;
    });

    const controllers: AbortController[] = [];
    let cancelled = false;

    const pollOnce = () => {
      for (const host of hosts) {
        const ctl = new AbortController();
        controllers.push(ctl);
        const timeout = window.setTimeout(() => ctl.abort(), REQUEST_TIMEOUT_MS);

        fetchOverview(host, ctl.signal)
          .then((data) => {
            if (cancelled) return;
            setStatuses((prev) => ({
              ...prev,
              [host.id]: { state: 'online', data, lastUpdated: Date.now() },
            }));
          })
          .catch((err) => {
            if (cancelled || ctl.signal.aborted) return;
            setStatuses((prev) => ({
              ...prev,
              [host.id]: {
                state: 'error',
                error: err instanceof Error ? err.message : String(err),
                lastUpdated: Date.now(),
              },
            }));
          })
          .finally(() => {
            window.clearTimeout(timeout);
          });
      }
    };

    pollOnce();
    const interval = window.setInterval(pollOnce, POLL_INTERVAL_MS);

    return () => {
      cancelled = true;
      window.clearInterval(interval);
      for (const ctl of controllers) {
        ctl.abort();
      }
    };
  }, [hosts]);

  return statuses;
}
