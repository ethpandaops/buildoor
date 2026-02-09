import React, { useState } from 'react';
import type { Stats, ServiceStatus } from '../types';

interface StatsPanelProps {
  stats: Stats | null;
  serviceStatus: ServiceStatus | null;
}

function formatGwei(gwei: number): string {
  if (gwei === 0) return '0';
  const eth = gwei / 1e9;
  if (eth >= 0.001) return eth.toFixed(4) + ' ETH';
  return gwei.toLocaleString() + ' Gwei';
}

export const StatsPanel: React.FC<StatsPanelProps> = ({ stats, serviceStatus }) => {
  const [collapsed, setCollapsed] = useState(false);

  const epbsAvailable = serviceStatus?.epbs_available ?? false;
  const legacyAvailable = serviceStatus?.legacy_available ?? false;

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} me-2`}></i>
        <h5 className="mb-0">Statistics</h5>
      </div>

      {!collapsed && (
        <div className="card-body">
          {/* Payload Builder */}
          <div className="section-header mb-2">Payload Builder</div>
          <div className="row g-2 mb-3">
            <div className="col-6">
              <div className="stat-item">
                <span className="stat-item-label">Payloads Built</span>
                <span className="stat-item-value">{stats?.slots_built || 0}</span>
              </div>
            </div>
            <div className="col-6">
              <div className="stat-item">
                <span className="stat-item-label">Payloads Won</span>
                <span className="stat-item-value">{stats?.blocks_included || 0}</span>
              </div>
            </div>
          </div>

          {/* ePBS Bidder */}
          {epbsAvailable && (
            <>
              <div className="section-header mb-2">ePBS Bidder</div>
              <div className="row g-2 mb-3">
                <div className="col-6">
                  <div className="stat-item">
                    <span className="stat-item-label">Submitted Bids</span>
                    <span className="stat-item-value">{stats?.bids_submitted || 0}</span>
                  </div>
                </div>
                <div className="col-6">
                  <div className="stat-item">
                    <span className="stat-item-label">Won Bids</span>
                    <span className="stat-item-value">{stats?.bids_won || 0}</span>
                  </div>
                </div>
                <div className="col-6">
                  <div className="stat-item">
                    <span className="stat-item-label">Payload Revealed</span>
                    <span className="stat-item-value">{stats?.reveals_success || 0}</span>
                  </div>
                </div>
                <div className="col-6">
                  <div className="stat-item">
                    <span className="stat-item-label">Total Paid</span>
                    <span className="stat-item-value">{formatGwei(stats?.total_paid || 0)}</span>
                  </div>
                </div>
              </div>
            </>
          )}

          {/* Builder API */}
          {legacyAvailable && (
            <>
              <div className="section-header mb-2">Builder API</div>
              <div className="row g-2">
                <div className="col-6">
                  <div className="stat-item">
                    <span className="stat-item-label">Registered Validators</span>
                    <span className="stat-item-value">{stats?.builder_api_registered_validators || 0}</span>
                  </div>
                </div>
                <div className="col-6">
                  <div className="stat-item">
                    <span className="stat-item-label">Requested Headers</span>
                    <span className="stat-item-value">{stats?.builder_api_headers_requested || 0}</span>
                  </div>
                </div>
                <div className="col-6">
                  <div className="stat-item">
                    <span className="stat-item-label">Published Blocks</span>
                    <span className="stat-item-value">{stats?.builder_api_blocks_published || 0}</span>
                  </div>
                </div>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
};
