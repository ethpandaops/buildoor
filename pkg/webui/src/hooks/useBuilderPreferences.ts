import { REFRESH_INTERVAL_SLOW_MS } from './refreshIntervals';
import { usePolledList } from './usePolledList';
import type { BuilderPreference, BuilderPreferencesResponse } from '../types';

// useBuilderPreferences lists the payment caps (max_execution_payment) that
// proposers submitted through the Gloas Builder API, highest cap first. A 404
// means the Builder API is not enabled.
export function useBuilderPreferences() {
  // Builder preferences change rarely; slow aligned background refresh.
  return usePolledList<BuilderPreference, BuilderPreferencesResponse>({
    url: '/api/buildoor/builder-preferences',
    intervalMs: REFRESH_INTERVAL_SLOW_MS,
    select: (body) => body.preferences,
    compare: (a, b) => b.max_execution_payment - a.max_execution_payment,
    what: 'payment caps',
  });
}
