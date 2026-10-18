import { useSyncExternalStore } from 'react';

export type ViewType =
  | 'overview'
  | 'dashboard'
  | 'action-plan'
  | 'bids-won'
  | 'mempool'
  | 'builder-keys'
  | 'registrations'
  | 'audit-log'
  | 'api-docs'
  // The login gate of the server's /debug/pprof pages (the server serves
  // the app there to a browser without the login cookie); not in the nav.
  | 'debug-login';

const VIEW_PATHS: Record<ViewType, string> = {
  overview: '/',
  dashboard: '/dashboard',
  'action-plan': '/action-plan',
  'bids-won': '/bids-won',
  mempool: '/mempool',
  'builder-keys': '/builder-keys',
  registrations: '/registrations',
  'audit-log': '/audit-log',
  'api-docs': '/api-docs',
  'debug-login': '/debug/pprof',
};

// Paths of views that moved or were merged into another one; old links and
// bookmarks land on the successor (the registration lists became the tabs of
// one page, the public overview is the landing page at /).
const LEGACY_PATHS: Record<string, string> = {
  '/validators': '/registrations',
  '/proposer-preferences': '/registrations/proposer-preferences',
  '/builder-preferences': '/registrations/payment-caps',
  '/overview': '/',
};

const PATH_TO_VIEW: Record<string, ViewType> = {};
for (const [view, path] of Object.entries(VIEW_PATHS)) {
  PATH_TO_VIEW[path] = view as ViewType;
}

function normalizePath(pathname: string): string {
  const trimmed = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname;
  return LEGACY_PATHS[trimmed] ?? trimmed;
}

// A view owns its path and every sub path below it (e.g. the tabs of
// /registrations/<tab>); unknown paths fall back to the public overview.
function viewFromPath(pathname: string): ViewType {
  if (PATH_TO_VIEW[pathname]) return PATH_TO_VIEW[pathname];
  for (const [path, view] of Object.entries(PATH_TO_VIEW)) {
    if (path !== '/' && pathname.startsWith(path + '/')) return view;
  }
  return 'overview';
}

// The server's own pages the app stands in for (the /debug/pprof login gate)
// keep their exact URL: the gate reloads it once the login cookie is set, and
// /debug/pprof/ (the index) is not /debug/pprof.
const SERVER_PATH_PREFIX = '/debug/';

let currentPath = normalizePath(window.location.pathname);
if (currentPath !== window.location.pathname && !window.location.pathname.startsWith(SERVER_PATH_PREFIX)) {
  history.replaceState(null, '', currentPath + window.location.search);
}
let currentView: ViewType = viewFromPath(currentPath);
const listeners = new Set<() => void>();

function emitChange() {
  listeners.forEach((listener) => listener());
}

export function getView() {
  return currentView;
}

export function getPath() {
  return currentPath;
}

export function getViewPath(view: ViewType): string {
  return VIEW_PATHS[view];
}

// navigate switches to a path inside the app (a view path or a sub path of
// one) and records it in the browser history.
export function navigate(path: string) {
  const normalized = normalizePath(path);
  if (normalized === currentPath) return;
  currentPath = normalized;
  currentView = viewFromPath(normalized);
  history.pushState(null, '', normalized);
  window.scrollTo(0, 0);
  emitChange();
}

export function setView(view: ViewType) {
  navigate(VIEW_PATHS[view]);
}

// Handle browser back/forward
window.addEventListener('popstate', () => {
  const path = normalizePath(window.location.pathname);
  if (currentPath === path) return;
  currentPath = path;
  currentView = viewFromPath(path);
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

// usePath returns the current app path, for views with sub paths.
export function usePath() {
  return useSyncExternalStore(subscribe, getPath);
}
