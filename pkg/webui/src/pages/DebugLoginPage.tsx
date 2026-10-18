import React, { useEffect, useState } from 'react';
import { useAuthContext } from '../context/AuthContext';

// The server's /debug/pprof pages need a login. Tools send the bearer token;
// a browser gets this page instead, which hands the session's token to the
// server as a short-lived cookie scoped to /debug/pprof and reloads. Must
// match debugTokenCookie in webui.go.
const DEBUG_TOKEN_COOKIE = 'buildoor_debug_token';
// The token tried last (its signature tail, never the token) and when: a
// token the server turned down right after its cookie was set must not
// reload the page in a loop. An older attempt is a cookie that expired
// before its token, which is simply set again.
const ATTEMPT_KEY = 'buildoor_debug_token_attempt';
const REFUSED_WITHIN_MS = 15_000;
// The cookie never outlives the token, nor this cap.
const MAX_COOKIE_AGE_SECONDS = 30 * 60;

function readAttempt(): string | null {
  try {
    return sessionStorage.getItem(ATTEMPT_KEY);
  } catch {
    return null;
  }
}

function writeAttempt(value: string) {
  try {
    sessionStorage.setItem(ATTEMPT_KEY, value);
  } catch {
    // Without storage a refused token shows the error after one reload.
  }
}

const DebugLoginPage: React.FC = () => {
  const { authEnabled, isLoggedIn, loading, expiresAt, getAuthHeader } = useAuthContext();
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (loading || !isLoggedIn) return;

    let cancelled = false;

    void (async () => {
      const token = await getAuthHeader();
      if (cancelled) return;

      if (!token) {
        setError(
          authEnabled
            ? 'Your session did not provide a login token.'
            : 'This server needs no login, but did not serve the page; try reloading.',
        );
        return;
      }

      const tail = token.slice(-16);
      const [lastTail, lastAt] = (readAttempt() ?? '').split(':');
      if (lastTail === tail && Date.now() - Number(lastAt) < REFUSED_WITHIN_MS) {
        setError('The server did not accept your login for the profiling pages.');
        return;
      }
      writeAttempt(`${tail}:${Date.now()}`);

      const remaining = expiresAt ? Math.floor((expiresAt - Date.now()) / 1000) : MAX_COOKIE_AGE_SECONDS;
      const maxAge = Math.max(1, Math.min(remaining, MAX_COOKIE_AGE_SECONDS));
      const secure = window.location.protocol === 'https:' ? '; Secure' : '';
      document.cookie = `${DEBUG_TOKEN_COOKIE}=${token}; Path=/debug/pprof; Max-Age=${maxAge}; SameSite=Strict${secure}`;
      window.location.reload();
    })();

    return () => {
      cancelled = true;
    };
  }, [authEnabled, isLoggedIn, loading, expiresAt, getAuthHeader]);

  return (
    <div className="card mx-auto mt-4" style={{ maxWidth: 560 }}>
      <div className="card-body">
        <h5 className="card-title">Profiling</h5>
        {error ? (
          <div className="alert alert-danger small mb-0">{error}</div>
        ) : loading || isLoggedIn ? (
          <p className="text-muted mb-0">
            <span className="spinner-border spinner-border-sm me-2" role="status"></span>
            Opening the profiling pages…
          </p>
        ) : (
          <p className="mb-0">
            The profiling pages need a login. Log in with the button in the header; this page continues
            on its own afterwards.
          </p>
        )}
      </div>
    </div>
  );
};

export default DebugLoginPage;
