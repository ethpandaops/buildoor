// Runtime config injected by the Go backend into <head> of index.html
// (see pkg/webui/handlers/spa.go injectHead). Exposed at boot via
// window.ethpandaops.buildoor.config so the SPA can read it synchronously
// without a roundtrip. The global Window shape is declared in
// stores/authStore.ts; this module just exposes a typed accessor.
export interface RuntimeConfig {
  authProviderURL: string;
  // The name this instance has in a login token's "services" claim
  // (--auth-service); the UI evaluates the claim against it.
  authService: string;
  overviewURL: string;
  version: string;
}

export function getRuntimeConfig(): RuntimeConfig {
  const cfg = (window.ethpandaops?.buildoor?.config ?? {}) as Partial<RuntimeConfig>;
  return {
    authProviderURL: cfg.authProviderURL ?? '',
    authService: cfg.authService || 'buildoor',
    overviewURL: cfg.overviewURL ?? '',
    version: cfg.version ?? '',
  };
}
