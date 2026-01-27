import React from 'react';
import { useAuthContext } from '../context/AuthContext';

export const UserDisplay: React.FC = () => {
  const { isLoggedIn, user, loading, login } = useAuthContext();

  if (loading) {
    return (
      <div className="d-flex align-items-center">
        <span className="spinner-border spinner-border-sm text-secondary" role="status">
          <span className="visually-hidden">Loading...</span>
        </span>
      </div>
    );
  }

  if (!isLoggedIn) {
    return (
      <button
        type="button"
        className="btn btn-outline-light btn-sm d-flex align-items-center gap-2"
        onClick={login}
      >
        <i className="fas fa-sign-in-alt"></i>
        <span>Login</span>
      </button>
    );
  }

  return (
    <div className="d-flex align-items-center gap-2 text-light">
      <i className="fas fa-user-circle"></i>
      <span className="user-name">{user}</span>
    </div>
  );
};
