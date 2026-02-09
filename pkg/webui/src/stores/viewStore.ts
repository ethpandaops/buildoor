import { useSyncExternalStore } from 'react';

export type ViewType = 'dashboard' | 'bids-won' | 'validators' | 'api-docs';

let currentView: ViewType = 'dashboard';
const listeners = new Set<() => void>();

function emitChange() {
  listeners.forEach((listener) => listener());
}

export function getView() {
  return currentView;
}

export function setView(view: ViewType) {
  if (currentView === view) return;
  currentView = view;
  emitChange();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useView() {
  return useSyncExternalStore(subscribe, getView);
}
