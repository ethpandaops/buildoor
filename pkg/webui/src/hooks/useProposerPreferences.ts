import { REFRESH_INTERVAL_LIVE_MS } from './refreshIntervals';
import { usePolledList } from './usePolledList';
import type { ProposerPreference, ProposerPreferencesResponse } from '../types';

// useProposerPreferences lists the cached gossip proposer preferences, newest
// slot first. A 404 means the proposer preferences service is not running.
export function useProposerPreferences() {
  // Preferences change slot-by-slot; live tier, only while the page is open.
  return usePolledList<ProposerPreference, ProposerPreferencesResponse>({
    url: '/api/buildoor/proposer-preferences',
    intervalMs: REFRESH_INTERVAL_LIVE_MS,
    select: (body) => body.preferences,
    compare: (a, b) => b.slot - a.slot,
    what: 'proposer preferences',
  });
}
