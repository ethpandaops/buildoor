import React from 'react';
import { createRoot } from 'react-dom/client';
import 'bootstrap/dist/css/bootstrap.min.css';
import 'bootstrap/dist/js/bootstrap.bundle.min.js';
import './styles.css';
import { OverviewApp } from './overview/OverviewApp';

const container = document.getElementById('react-app');
if (container) {
  const root = createRoot(container);
  root.render(
    <React.StrictMode>
      <OverviewApp />
    </React.StrictMode>
  );
}
