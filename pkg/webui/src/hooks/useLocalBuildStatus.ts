import { useCallback } from 'react';
import type { LocalBuildAvailability } from '../types';
import { authHeaders } from './authFetch';
import { useEventStream } from './useEventStream';
import { apiPath } from '../utils/api';

// The local build (testing_buildBlockV1) status: the probed EL availability,
// the effective settings and the pool's state, pushed by the event stream at
// connect and on every change. probe() forces a fresh EL probe
// (authenticated); its result arrives through the stream as well.
export function useLocalBuildStatus() {
  const { localBuildStatus } = useEventStream();

  const probe = useCallback(async (): Promise<LocalBuildAvailability | null> => {
    const response = await fetch(apiPath('/local-build/probe'), {
      method: 'POST',
      headers: await authHeaders(),
    });
    const data = await response.json();
    if (!response.ok) {
      throw new Error(data.error || response.statusText);
    }
    return data as LocalBuildAvailability;
  }, []);

  return { status: localBuildStatus, loading: localBuildStatus === null, probe };
}
