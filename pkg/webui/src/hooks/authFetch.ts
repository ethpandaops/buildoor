import { useSyncExternalStore } from 'react';
import { authStore } from '../stores/authStore';
import type { LocalBuildPlan, SlotPlan, SlotRule } from '../types';

// authHeaders returns the login Authorization header (empty when logged out
// or in open mode), plus any extra headers.
export async function authHeaders(extra?: Record<string, string>): Promise<Record<string, string>> {
  const headers: Record<string, string> = { ...(extra ?? {}) };
  const token = await authStore.getAuthHeader();
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }
  return headers;
}

const subscribeAuth = (onChange: () => void) => authStore.subscribe(onChange);
const getLoggedIn = () => authStore.getState().isLoggedIn;

// useLoggedIn tracks the login state. Data hooks depend on it to refetch once
// the auth client settles (the first fetch can run before a token exists) or
// a login lands from another tab: reads without a login come back redacted.
export function useLoggedIn(): boolean {
  return useSyncExternalStore(subscribeAuth, getLoggedIn);
}

function localRedacted(local: LocalBuildPlan | undefined): boolean {
  return !!local?.transactions_redacted && !local.transactions?.length;
}

// Readers without a login (and every SSE event) get explicit local build
// transaction lists replaced by their length; these report whether an object
// carries such a hidden list.
export function planRedacted(plan: SlotPlan | SlotRule | null | undefined): boolean {
  return localRedacted(plan?.build?.local);
}
