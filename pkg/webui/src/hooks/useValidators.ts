import { REFRESH_INTERVAL_SLOW_MS } from './refreshIntervals';
import { usePolledList } from './usePolledList';
import type { ValidatorRegistration, ValidatorsResponse } from '../types';

// useValidators lists the Builder API validator registrations, newest
// registration first.
export function useValidators() {
  return usePolledList<ValidatorRegistration, ValidatorsResponse>({
    url: '/api/buildoor/validators',
    intervalMs: REFRESH_INTERVAL_SLOW_MS,
    select: (body) => body.validators,
    compare: (a, b) => b.timestamp - a.timestamp || a.pubkey.localeCompare(b.pubkey),
    withAuth: true,
    what: 'validator registrations',
  });
}
