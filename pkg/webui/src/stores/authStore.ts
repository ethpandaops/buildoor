import type { AuthState } from '../types';

// Singleton auth store. Drives authentication via the centralized
// authenticatoor service when window.ethpandaops.buildoor.config
// .authProviderURL is set (injected by the backend into index.html);
// otherwise runs in "open" mode (no auth required).
//
// Public surface:
//   authStore.initialize(): kicks off boot — must be called once at app start.
//   authStore.getState(): synchronous current state.
//   authStore.subscribe(fn): change subscription, returns unsubscribe.
//   authStore.getAuthHeader(): bearer token for API calls, or null.
//   authStore.login(): full-page redirect to authenticatoor /auth/login.
//   authStore.logout(): clear local session.
//   authStore.fetchToken(): re-attempt the iframe path; returns new state.
//
// In open mode:
//   - authEnabled = false, isLoggedIn = true (UI treats user as authorized)
//   - getAuthHeader returns null (no Authorization header on requests)
//   - login()/logout() are no-ops (and the UI should hide login controls)

const AUTH_STATE_CHANGE_EVENT = 'buildoor_auth_state_change';

// Minimal shape of window.ethpandaops.authenticatoor we rely on.
interface AuthenticatoorLib {
  checkLogin: () => Promise<{
    authenticated: boolean;
    token: string;
    exp: number;
    user: string;
  }>;
  login: () => void;
  logout: () => void;
  getToken: () => string | null;
  isLoggedIn: () => boolean;
  authServiceURL: () => string;
}

interface RuntimeConfig {
  authProviderURL: string;
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
  token: null,
  expiresAt: null,
};

const ANON_STATE: AuthState = {
  authEnabled: true,
  isLoggedIn: false,
  user: null,
  token: null,
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
    return {
      authProviderURL: window.ethpandaops?.buildoor?.config?.authProviderURL ?? '',
    };
  }

  private loadAuthScript(authProviderURL: string): Promise<void> {
    return new Promise((resolve, reject) => {
      // Already loaded?
      if (window.ethpandaops?.authenticatoor) {
        resolve();
        return;
      }
      const script = document.createElement('script');
      script.src = authProviderURL.replace(/\/+$/, '') + '/client.js';
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

      // Remote mode — load the authenticatoor client library and run its
      // checkLogin (fragment → cache → silent iframe up to 30s). Treat the
      // user as anonymous in the meantime; re-render when the promise
      // resolves with authenticated=true.
      this.setState({ ...ANON_STATE });

      try {
        await this.loadAuthScript(cfg.authProviderURL);
      } catch (e) {
        console.error('authStore: failed to load client.js', e);
        this.initialized = true;
        return;
      }

      this.lib = window.ethpandaops?.authenticatoor ?? null;
      if (!this.lib) {
        console.error('authStore: ethpandaops.authenticatoor not available after load');
        this.initialized = true;
        return;
      }

      try {
        const info = await this.lib.checkLogin();
        if (info.authenticated) {
          this.setState({
            authEnabled: true,
            isLoggedIn: true,
            user: info.user || null,
            token: info.token,
            expiresAt: info.exp * 1000,
          });
        }
      } catch (e) {
        console.error('authStore: checkLogin failed', e);
      }

      this.initialized = true;
    })();

    return this.initPromise;
  }

  /**
   * Re-attempt the silent token acquisition path. Useful when an API call
   * comes back 401 — the token may have expired.
   */
  async fetchToken(): Promise<AuthState> {
    if (!this.state.authEnabled || !this.lib) return this.state;
    try {
      const info = await this.lib.checkLogin();
      if (info.authenticated) {
        this.setState({
          authEnabled: true,
          isLoggedIn: true,
          user: info.user || null,
          token: info.token,
          expiresAt: info.exp * 1000,
        });
      } else {
        this.setState({ ...ANON_STATE });
      }
    } catch {
      this.setState({ ...ANON_STATE });
    }
    return this.state;
  }

  getState(): AuthState {
    return this.state;
  }

  /**
   * Returns the bearer token to attach to API calls, or null when no auth
   * is required (open mode) or the user isn't authenticated yet.
   */
  getAuthHeader(): string | null {
    if (!this.state.authEnabled) return null;
    if (!this.state.token) return null;
    if (this.state.expiresAt && this.state.expiresAt < Date.now()) return null;
    return this.state.token;
  }

  subscribe(listener: AuthStateListener): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  login(): void {
    if (!this.state.authEnabled) return;
    this.lib?.login();
  }

  logout(): void {
    if (!this.state.authEnabled) return;
    this.lib?.logout();
    this.setState({ ...ANON_STATE });
  }

  destroy(): void {
    window.removeEventListener(AUTH_STATE_CHANGE_EVENT, this.handleExternalStateChange);
    this.listeners.clear();
  }
}

export const authStore = new AuthStore();
