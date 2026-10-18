import React from 'react';
import { createRoot } from 'react-dom/client';
import 'bootstrap/dist/css/bootstrap.min.css';
import 'bootstrap/dist/js/bootstrap.bundle.min.js';
import '@fortawesome/fontawesome-free/css/all.min.css';
import { App } from './App';
import { AuthProvider } from './context/AuthContext';
import { EventStreamProvider } from './hooks/useEventStream';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-sans/600.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';
import './styles.css';
import './styles/tokens.css';

// Render main app
const appContainer = document.getElementById('react-app');
if (appContainer) {
  const appRoot = createRoot(appContainer);
  appRoot.render(
    <React.StrictMode>
      <AuthProvider>
        <EventStreamProvider>
          <App />
        </EventStreamProvider>
      </AuthProvider>
    </React.StrictMode>
  );
}
