import React from 'react';
import { createRoot } from 'react-dom/client';
import 'bootstrap/dist/css/bootstrap.min.css';
import 'bootstrap/dist/js/bootstrap.bundle.min.js';
import '@fortawesome/fontawesome-free/css/all.min.css';
import { App } from './App';
import { AuthProvider } from './context/AuthContext';
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
