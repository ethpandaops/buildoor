import React from 'react';
import { useOverviewHosts, useOverviewPolling } from './useOverview';
import { InstanceCard } from './InstanceCard';
import { BrandHeader } from '../components/BrandHeader';

export const OverviewApp: React.FC = () => {
  const { hosts, error: hostsError } = useOverviewHosts();
  const statuses = useOverviewPolling(hosts);

  return (
    <>
      <BrandHeader title="Buildoor Overview" />

      <main className="container mt-3 app-main">
        <div className="d-flex align-items-baseline flex-wrap gap-2 mb-3">
          <h1 className="h4 mb-0">Builder Instances</h1>
          <span className="text-secondary small">
            Aggregated view of multiple buildoor instances · refreshes every minute
          </span>
        </div>

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

        {hosts && hosts.length > 0 && (
          <div className="d-flex flex-column gap-3">
            {hosts.map((host) => (
              <InstanceCard
                key={host.id}
                host={host}
                status={statuses[host.id] ?? { state: 'loading' }}
              />
            ))}
          </div>
        )}
      </main>
    </>
  );
};
