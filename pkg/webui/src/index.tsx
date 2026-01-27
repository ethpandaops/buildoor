import React from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { AuthProvider } from './context/AuthContext';
import { UserDisplay } from './components/UserDisplay';
import './styles.css';

// Render main app
const appContainer = document.getElementById('react-app');
if (appContainer) {
  const appRoot = createRoot(appContainer);
  appRoot.render(
    <React.StrictMode>
      <AuthProvider>
        <App />
      </AuthProvider>
    </React.StrictMode>
  );
}

// Render user display in header
const userDisplayContainer = document.getElementById('user-display');
if (userDisplayContainer) {
  const userRoot = createRoot(userDisplayContainer);
  userRoot.render(
    <React.StrictMode>
      <AuthProvider>
        <UserDisplay />
      </AuthProvider>
    </React.StrictMode>
  );
}
