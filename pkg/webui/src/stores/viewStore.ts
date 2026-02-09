import { useSyncExternalStore } from 'react';

export type ViewType = 'dashboard' | 'bids-won' | 'validators' | 'api-docs';

const VIEW_PATHS: Record<ViewType, string> = {
  dashboard: '/',
  'bids-won': '/bids-won',
  validators: '/validators',
  'api-docs': '/api-docs',
};

const PATH_TO_VIEW: Record<string, ViewType> = {};
for (const [view, path] of Object.entries(VIEW_PATHS)) {
  PATH_TO_VIEW[path] = view as ViewType;
}

function viewFromPath(pathname: string): ViewType {
  return PATH_TO_VIEW[pathname] ?? 'dashboard';
}

let currentView: ViewType = viewFromPath(window.location.pathname);
const listeners = new Set<() => void>();

function emitChange() {
  listeners.forEach((listener) => listener());
}

export function getView() {
  return currentView;
}

export function getViewPath(view: ViewType): string {
  return VIEW_PATHS[view];
}

export function setView(view: ViewType) {
  if (currentView === view) return;
  currentView = view;
  history.pushState(null, '', VIEW_PATHS[view]);
  emitChange();
}

// Handle browser back/forward
window.addEventListener('popstate', () => {
  const view = viewFromPath(window.location.pathname);
  if (currentView === view) return;
  currentView = view;
  emitChange();
});

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useView() {
  return useSyncExternalStore(subscribe, getView);
}
