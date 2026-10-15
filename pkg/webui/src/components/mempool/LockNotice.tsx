import React from 'react';
import { useAuthContext } from '../../context/AuthContext';

// LockNotice replaces private pool content (transactions, callers, keys)
// for readers without a login, with a login action when auth is configured.
export const LockNotice: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const { authEnabled, login } = useAuthContext();
  return (
    <div className="bd-notice bd-mp-lock">
      <i className="fas fa-lock"></i>
      <span>{children}</span>
      {authEnabled && (
        <button type="button" className="bd-btn bd-btn-sm bd-btn-primary ms-auto" onClick={login}>
          Log in
        </button>
      )}
    </div>
  );
};

// ErrorNotice shows an inline error with a dismiss action.
export const ErrorNotice: React.FC<{ message: string | null; onDismiss?: () => void }> = ({ message, onDismiss }) => {
  if (!message) return null;
  return (
    <div className="bd-mp-error" role="alert">
      <i className="fas fa-triangle-exclamation"></i>
      <span className="flex-grow-1">{message}</span>
      {onDismiss && (
        <button type="button" className="btn-close btn-sm" aria-label="Dismiss" onClick={onDismiss}></button>
      )}
    </div>
  );
};
