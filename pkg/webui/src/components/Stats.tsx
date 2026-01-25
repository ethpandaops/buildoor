import React from 'react';
import type { Stats as StatsType } from '../types';

interface StatsProps {
  stats: StatsType | null;
}

export const Stats: React.FC<StatsProps> = ({ stats }) => {
  return (
    <div className="card mb-3">
      <div className="card-header">
        <h5 className="mb-0">Statistics</h5>
      </div>
      <div className="card-body">
        <div className="row g-2">
          <div className="col-6">
            <div className="stat-box">
              <div className="stat-value">{stats?.slots_built || 0}</div>
              <div className="stat-label">Slots Built</div>
            </div>
          </div>
          <div className="col-6">
            <div className="stat-box">
              <div className="stat-value">{stats?.bids_submitted || 0}</div>
              <div className="stat-label">Bids Submitted</div>
            </div>
          </div>
          <div className="col-6">
            <div className="stat-box">
              <div className="stat-value">{stats?.bids_won || 0}</div>
              <div className="stat-label">Bids Won</div>
            </div>
          </div>
          <div className="col-6">
            <div className="stat-box">
              <div className="stat-value">{stats?.reveals_success || 0}</div>
              <div className="stat-label">Reveals Success</div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
};
