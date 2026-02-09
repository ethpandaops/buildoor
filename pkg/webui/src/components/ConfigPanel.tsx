import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { Config, EPBSConfig, ServiceStatus } from '../types';

interface ConfigPanelProps {
  config: Config | null;
  serviceStatus: ServiceStatus | null;
}

type EPBSFormState = EPBSConfig;

export const ConfigPanel: React.FC<ConfigPanelProps> = ({ config, serviceStatus }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [collapsed, setCollapsed] = useState(true);
  const [editingTiming, setEditingTiming] = useState(false);
  const [toggling, setToggling] = useState(false);

  const [timingForm, setTimingForm] = useState<EPBSFormState>({
    build_start_time: 0,
    bid_start_time: 0,
    bid_end_time: 0,
    reveal_time: 0,
    bid_min_amount: 0,
    bid_increase: 0,
    bid_interval: 0,
  });

  // Sync timing form state when not editing
  useEffect(() => {
    if (!editingTiming && config?.epbs) {
      setTimingForm({ ...config.epbs });
    }
  }, [config, editingTiming]);

  const handleTimingSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const authToken = getAuthHeader();
    if (!authToken) {
      alert('You must be logged in to update configuration');
      return;
    }
    try {
      const response = await fetch('/api/config/epbs', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${authToken}`,
        },
        body: JSON.stringify({
          bid_start_time: timingForm.bid_start_time,
          bid_end_time: timingForm.bid_end_time,
          reveal_time: timingForm.reveal_time,
          bid_min_amount: timingForm.bid_min_amount,
          bid_increase: timingForm.bid_increase,
          bid_interval: timingForm.bid_interval,
        }),
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to update: ' + result.error);
      } else {
        setEditingTiming(false);
      }
    } catch (err) {
      alert('Error: ' + err);
    }
  };

  const canEdit = isLoggedIn;
  const epbs = config?.epbs;
  const isActive = serviceStatus?.epbs_enabled ?? false;
  const isAvailable = serviceStatus?.epbs_available ?? false;
  const registrationState = serviceStatus?.epbs_registration_state ?? 'unknown';
  const isRegistered = registrationState === 'registered';

  const handleToggle = async (e: React.MouseEvent) => {
    e.stopPropagation();
    const authToken = getAuthHeader();
    if (!authToken) return;
    setToggling(true);
    try {
      await fetch('/api/services/toggle', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${authToken}`,
        },
        body: JSON.stringify({ epbs_enabled: !isActive }),
      });
    } catch (err) {
      console.error('Failed to toggle ePBS:', err);
    } finally {
      setToggling(false);
    }
  };

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} me-2`}></i>
        <h5 className="mb-0 me-2">ePBS Bidder</h5>
        {!isAvailable ? (
          <span className="badge bg-dark">Not Available</span>
        ) : isActive && isRegistered ? (
          <span className="badge bg-success">Active</span>
        ) : isActive && !isRegistered ? (
          <span className="badge bg-warning text-dark">Pending Registration</span>
        ) : (
          <span className="badge bg-secondary">Inactive</span>
        )}
        {canEdit && (
          <button
            className={`btn btn-sm ms-auto ${isActive ? 'btn-outline-danger' : 'btn-outline-success'}`}
            onClick={handleToggle}
            disabled={toggling || !isAvailable}
            title={!isAvailable ? 'ePBS not available (Gloas fork not scheduled)' : isActive ? 'Disable ePBS' : 'Enable ePBS'}
          >
            <i className={`fas ${isActive ? 'fa-pause' : 'fa-play'}`}></i>
          </button>
        )}
      </div>

      {!collapsed && (
        <div className="card-body">
          {!isAvailable && (
            <div className="alert alert-secondary small mb-2 py-1 px-2">
              <i className="fas fa-info-circle me-1"></i>
              ePBS is not available. The connected beacon node does not have the Gloas fork scheduled.
            </div>
          )}
          {isAvailable && !isRegistered && registrationState === 'waiting_gloas' && (
            <div className="alert alert-info small mb-2 py-1 px-2">
              <i className="fas fa-clock me-1"></i>
              Waiting for Gloas fork activation before builder registration.
            </div>
          )}
          {isAvailable && !isRegistered && (registrationState === 'pending' || registrationState === 'unknown') && (
            <div className="alert alert-warning small mb-2 py-1 px-2">
              <i className="fas fa-spinner fa-spin me-1"></i>
              Builder registration in progress. Bidding will start after registration completes.
            </div>
          )}
          {/* Timing Config Section */}
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Timing Config</div>
            {!editingTiming && (
              <button
                className="btn btn-sm btn-outline-primary"
                onClick={() => setEditingTiming(true)}
                disabled={!canEdit}
                title={!canEdit ? 'Login required to edit' : ''}
              >
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editingTiming ? (
            <div className="row g-2">
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Start</div>
                  <div className="config-item-value">{epbs?.bid_start_time || 0} ms</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid End</div>
                  <div className="config-item-value">{epbs?.bid_end_time || 0} ms</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Reveal Time</div>
                  <div className="config-item-value">{epbs?.reveal_time || 0} ms</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Min</div>
                  <div className="config-item-value">{epbs?.bid_min_amount || 0} gwei</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Increase</div>
                  <div className="config-item-value">{epbs?.bid_increase || 0} gwei</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Interval</div>
                  <div className="config-item-value">{epbs?.bid_interval || 0} ms</div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleTimingSave}>
              <div className="mb-2">
                <label className="form-label">Bid Start Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_start_time}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_start_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid End Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_end_time}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_end_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Reveal Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.reveal_time}
                  onChange={(e) => setTimingForm({ ...timingForm, reveal_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Min Amount (gwei)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_min_amount}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_min_amount: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Increase (gwei)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_increase}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_increase: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Interval (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_interval}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_interval: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditingTiming(false)}>
                  Cancel
                </button>
              </div>
            </form>
          )}
        </div>
      )}
    </div>
  );
};
