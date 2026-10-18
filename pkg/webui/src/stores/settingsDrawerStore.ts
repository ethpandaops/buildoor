import { useSyncExternalStore } from 'react';

// Flows whose settings the drawer edits. Each has a definition in
// settings/flows.ts (tabs, fields, summary rows).
export type FlowId =
  | 'engine'
  | 'testing'
  | 'keys'
  | 'lifecycle'
  | 'epbs'
  | 'builder-api'
  | 'reveal'
  | 'mempool';

export interface SettingsDrawerState {
  flow: FlowId | null;
  tab: string | null;
}

let state: SettingsDrawerState = { flow: null, tab: null };
const listeners = new Set<() => void>();

function emit() {
  listeners.forEach((listener) => listener());
}

// Opens the settings drawer of a flow, optionally on a specific tab. The
// drawer is rendered once by the app shell, so any page can open it.
export function openSettings(flow: FlowId, tab?: string) {
  state = { flow, tab: tab ?? null };
  emit();
}

export function closeSettings() {
  if (!state.flow) return;
  state = { flow: null, tab: null };
  emit();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function getState() {
  return state;
}

export function useSettingsDrawer(): SettingsDrawerState {
  return useSyncExternalStore(subscribe, getState);
}
