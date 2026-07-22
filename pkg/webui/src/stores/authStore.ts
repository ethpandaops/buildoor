import type { AuthState } from '../types';

// Singleton auth store. Drives authentication via the centralized
// authenticatoor service (v2 shared-session client) when
// window.ethpandaops.buildoor.config.authProviderURL is set (injected by
// the backend into index.html); otherwise runs in "open" mode (no auth
// required).
//
// v2 model: the authenticatoor client mounts a hidden iframe on the auth
// service origin which owns the session — it refreshes tokens before
// expiry and keeps login state in sync across every ethpandaops app and
// tab. This store simply mirrors the client's "status" events into React
// state and asks the client for a fresh token on every API call. No token
// is ever cached in the app.
//
// Public surface:
//   authStore.initialize(): kicks off boot — must be called once at app start.
//   authStore.getState(): synchronous current state.
//   authStore.subscribe(fn): change subscription, returns unsubscribe.
//   authStore.getAuthHeader(): Promise<string|null> — fresh bearer token
//     for API calls (fetched from the auth client every time), or null.
//   authStore.login(): full-page redirect to authenticatoor /auth/login
//     (resolves without navigating when already authenticated).
//   authStore.logout(): global logout — all apps/tabs converge.
//   authStore.fetchToken(): force a token fetch; returns new state.
//
// In open mode:
//   - authEnabled = false, isLoggedIn = true (UI treats user as authorized)
//   - getAuthHeader resolves null (no Authorization header on requests)
//   - login()/logout() are no-ops (and the UI should hide login controls)

const AUTH_STATE_CHANGE_EVENT = 'buildoor_auth_state_change';

// TokenInfo pushed by the v2 client on every session change.
export interface AuthTokenInfo {
  status: 'unauthenticated' | 'authenticated' | 'refreshing';
  authenticated: boolean;
  user: string;
  exp: number;
}

// Minimal shape of the v2 window.ethpandaops.authenticatoor we rely on.
interface AuthenticatoorLib {
  version?: number;
  addEventListener: (type: 'status', cb: (info: AuthTokenInfo) => void) => void;
  removeEventListener: (type: 'status', cb: (info: AuthTokenInfo) => void) => void;
  getStatus: () => Promise<AuthTokenInfo>;
  getToken: () => Promise<string | null>;
  login: () => Promise<boolean>;
  logout: () => Promise<void>;
  authServiceURL: () => string;
}

interface RuntimeConfig {
  authProviderURL: string;
  overviewURL: string;
}

declare global {
  interface Window {
    ethpandaops?: {
      authenticatoor?: AuthenticatoorLib;
      buildoor?: {
        config?: Partial<RuntimeConfig>;
      };
    };
  }
}

type AuthStateListener = (state: AuthState) => void;

const OPEN_STATE: AuthState = {
  authEnabled: false,
  isLoggedIn: true, // open mode: treat as authorized
  user: null,
  expiresAt: null,
};

const ANON_STATE: AuthState = {
  authEnabled: true,
  isLoggedIn: false,
  user: null,
  expiresAt: null,
};

class AuthStore {
  private state: AuthState = { ...ANON_STATE };
  private listeners = new Set<AuthStateListener>();
  private initialized = false;
  private initPromise: Promise<void> | null = null;
  private lib: AuthenticatoorLib | null = null;

  constructor() {
    window.addEventListener(AUTH_STATE_CHANGE_EVENT, this.handleExternalStateChange);
  }

  private handleExternalStateChange = (event: Event) => {
    const customEvent = event as CustomEvent<AuthState>;
    if (customEvent.detail) {
      this.state = customEvent.detail;
      this.notifyListeners();
    }
  };

  private setState(next: AuthState): void {
    this.state = next;
    this.notifyListeners();
    window.dispatchEvent(
      new CustomEvent(AUTH_STATE_CHANGE_EVENT, { detail: next })
    );
  }

  private notifyListeners(): void {
    this.listeners.forEach((l) => l(this.state));
  }

  private readRuntimeConfig(): RuntimeConfig {
    const cfg = window.ethpandaops?.buildoor?.config;
    return {
      authProviderURL: cfg?.authProviderURL ?? '',
      overviewURL: cfg?.overviewURL ?? '',
    };
  }

  // Mirror a TokenInfo pushed by the auth client into our state. The
  // "refreshing" status still carries authenticated=true while the old
  // token is valid, so the UI doesn't flicker during background refreshes.
  private applyTokenInfo = (info: AuthTokenInfo): void => {
    this.setState({
      authEnabled: true,
      isLoggedIn: info.authenticated,
      user: info.authenticated ? info.user || null : null,
      expiresAt: info.authenticated && info.exp ? info.exp * 1000 : null,
    });
  };

  private loadAuthScript(authProviderURL: string): Promise<void> {
    return new Promise((resolve, reject) => {
      // Already loaded?
      if (window.ethpandaops?.authenticatoor) {
        resolve();
        return;
      }
      const script = document.createElement('script');
      script.src = authProviderURL.replace(/\/+$/, '') + '/client-v2.js';
      script.async = true;
      script.onload = () => {
        if (window.ethpandaops?.authenticatoor) resolve();
        else reject(new Error('authenticatoor: client.js loaded but global missing'));
      };
      script.onerror = () => reject(new Error('authenticatoor: failed to load client.js'));
      document.head.appendChild(script);
    });
  }

  async initialize(): Promise<void> {
    if (this.initialized) return;
    if (this.initPromise) return this.initPromise;

    this.initPromise = (async () => {
      const cfg = this.readRuntimeConfig();

      if (!cfg.authProviderURL) {
        // Open mode — API is unauthenticated.
        this.setState({ ...OPEN_STATE });
        this.initialized = true;
        return;
      }

      // Remote mode — load the v2 authenticatoor client and mirror its
      // status events. Treat the user as anonymous until the first status
      // arrives (the client replays the current state on subscribe).
      this.setState({ ...ANON_STATE });

      try {
        await this.loadAuthScript(cfg.authProviderURL);
      } catch (e) {
        console.error('authStore: failed to load client.js', e);
        this.initialized = true;
        return;
      }

      this.lib = window.ethpandaops?.authenticatoor ?? null;
      if (!this.lib || typeof this.lib.addEventListener !== 'function') {
        console.error('authStore: ethpandaops.authenticatoor (v2) not available after load');
        this.lib = null;
        this.initialized = true;
        return;
      }

      // Every future session change (login/logout/refresh in ANY app or
      // tab) lands here and re-renders the top bar.
      this.lib.addEventListener('status', this.applyTokenInfo);

      try {
        // Settle the initial state before resolving initialization.
        this.applyTokenInfo(await this.lib.getStatus());
      } catch (e) {
        console.error('authStore: getStatus failed', e);
      }

      this.initialized = true;
    })();

    return this.initPromise;
  }

  /**
   * Force a token fetch through the auth client (the client refreshes via
   * its shared frame when needed). Useful when an API call comes back 401.
   */
  async fetchToken(): Promise<AuthState> {
    if (!this.state.authEnabled || !this.lib) return this.state;
    try {
      await this.lib.getToken();
      this.applyTokenInfo(await this.lib.getStatus());
    } catch {
      // Keep current state; the status listener reports the real outcome.
    }
    return this.state;
  }

  getState(): AuthState {
    return this.state;
  }

  /**
   * Returns the bearer token to attach to API calls, or null when no auth
   * is required (open mode) or the user isn't authenticated. Always asks
   * the auth client for a fresh token — never cached here — so a token
   * refreshed by the shared frame is picked up immediately.
   */
  async getAuthHeader(): Promise<string | null> {
    if (!this.state.authEnabled) return null;
    if (!this.lib) return null;
    try {
      return await this.lib.getToken();
    } catch {
      return null;
    }
  }

  subscribe(listener: AuthStateListener): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  login(): void {
    if (!this.state.authEnabled) return;
    void this.lib?.login();
  }

  logout(): void {
    if (!this.state.authEnabled) return;
    // Global logout: the shared frame clears the session everywhere; our
    // status listener flips the state when it lands. Set it eagerly too
    // so the UI reacts instantly.
    void this.lib?.logout();
    this.setState({ ...ANON_STATE });
  }

  destroy(): void {
    window.removeEventListener(AUTH_STATE_CHANGE_EVENT, this.handleExternalStateChange);
    this.lib?.removeEventListener('status', this.applyTokenInfo);
    this.listeners.clear();
  }
}

export const authStore = new AuthStore();
