import React from 'react';
import { useOverviewHosts, useOverviewPolling } from './useOverview';
import { InstanceTable } from './InstanceTable';
import { useTheme, type ThemeMode } from '../hooks/useTheme';

const THEME_LABEL: Record<ThemeMode, string> = {
  light: 'Light',
  dark: 'Dark',
  auto: 'Auto',
};

const THEME_ORDER: ThemeMode[] = ['light', 'dark', 'auto'];

export const OverviewApp: React.FC = () => {
  const { hosts, error: hostsError } = useOverviewHosts();
  const statuses = useOverviewPolling(hosts);
  const { theme, setTheme } = useTheme();

  return (
    <>
      <header className="text-bg-dark px-3 py-2 d-flex align-items-center" data-bs-theme="dark">
        <h1 className="h5 mb-0 me-3">Buildoor Overview</h1>
        <span className="text-secondary small">
          Aggregated view of multiple buildoor instances
        </span>
        <div className="ms-auto">
          <div className="btn-group btn-group-sm" role="group" aria-label="Theme">
            {THEME_ORDER.map((mode) => (
              <button
                key={mode}
                type="button"
                className={`btn ${theme === mode ? 'btn-primary' : 'btn-outline-secondary'}`}
                onClick={() => setTheme(mode)}
              >
                {THEME_LABEL[mode]}
              </button>
            ))}
          </div>
        </div>
      </header>

      <main className="container-fluid mt-3">
        {hostsError && (
          <div className="alert alert-danger" role="alert">
            Failed to load configured hosts: {hostsError}
          </div>
        )}

        {hosts === null && !hostsError && (
          <div className="text-muted text-center py-5">Loading…</div>
        )}

        {hosts && hosts.length === 0 && (
          <div className="alert alert-warning" role="alert">
            No buildoor instances configured. Pass <code>--host</code> when starting{' '}
            <code>buildoor overview</code>.
          </div>
        )}

        {hosts && hosts.length > 0 && <InstanceTable hosts={hosts} statuses={statuses} />}
      </main>
    </>
  );
};
