import React from 'react';
import { getRuntimeConfig } from '../utils/runtimeConfig';

// Footer shared by the dashboard and the overview UI, the same line dora and
// spamoor show: the project link and the build version.
export const Footer: React.FC = () => {
  const { version } = getRuntimeConfig();

  return (
    <footer className="app-footer container text-center text-secondary small">
      Powered by{' '}
      <a href="https://github.com/ethpandaops/buildoor" target="_blank" rel="noopener noreferrer">
        ethpandaops/buildoor
      </a>
      {version && <> | {version}</>}
    </footer>
  );
};
