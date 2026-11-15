import React, { createContext, useContext, type ReactNode } from 'react';
import { useAuth } from '../hooks/useAuth';
import type { FeatureSet } from '../utils/authFeatures';

interface AuthContextValue {
  authEnabled: boolean;
  isLoggedIn: boolean;
  user: string | null;
  expiresAt: number | null;
  features: FeatureSet;
  loading: boolean;
  getAuthHeader: () => Promise<string | null>;
  login: () => void;
  logout: () => void;
  refreshToken: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

interface AuthProviderProps {
  children: ReactNode;
}

export const AuthProvider: React.FC<AuthProviderProps> = ({ children }) => {
  const auth = useAuth();

  return (
    <AuthContext.Provider value={auth}>
      {children}
    </AuthContext.Provider>
  );
};

export function useAuthContext(): AuthContextValue {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error('useAuthContext must be used within an AuthProvider');
  }
  return context;
}
