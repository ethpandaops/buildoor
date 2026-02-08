import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { Config, EPBSConfig, ScheduleConfig, ServiceStatus, Stats } from '../types';

interface ConfigPanelProps {
  config: Config | null;
  serviceStatus: ServiceStatus | null;
  stats: Stats | null;
}

type EPBSFormState = EPBSConfig;

export const ConfigPanel: React.FC<ConfigPanelProps> = ({ config, serviceStatus, stats }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [collapsed, setCollapsed] = useState(false);
  const [editingTiming, setEditingTiming] = useState(false);
  const [editingSchedule, setEditingSchedule] = useState(false);

  const [timingForm, setTimingForm] = useState<EPBSFormState>({
    build_start_time: 0,
    bid_start_time: 0,
    bid_end_time: 0,
    reveal_time: 0,
    bid_min_amount: 0,
    bid_increase: 0,
    bid_interval: 0,
  });

  const [scheduleForm, setScheduleForm] = useState<ScheduleConfig>({
    mode: 'all',
    every_nth: 1,
    next_n: 0,
    start_slot: 0,
  });

  // Sync timing form state when not editing
  useEffect(() => {
    if (!editingTiming && config?.epbs) {
      setTimingForm({ ...config.epbs });
    }
  }, [config, editingTiming]);

  // Sync schedule form state when not editing
  useEffect(() => {
    if (!editingSchedule && config?.schedule) {
      setScheduleForm(config.schedule);
    }
  }, [config, editingSchedule]);

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

  const handleScheduleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const authToken = getAuthHeader();
    if (!authToken) {
      alert('You must be logged in to update configuration');
      return;
    }
    try {
      const response = await fetch('/api/config/schedule', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${authToken}`,
        },
        body: JSON.stringify(scheduleForm),
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to update: ' + result.error);
      } else {
        setEditingSchedule(false);
      }
    } catch (err) {
      alert('Error: ' + err);
    }
  };

  const canEdit = isLoggedIn;
  const epbs = config?.epbs;
  const schedule = config?.schedule;
  const isActive = serviceStatus?.epbs_enabled ?? false;

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} me-2`}></i>
        <h5 className="mb-0 me-2">ePBS</h5>
        <span className={`badge ${isActive ? 'bg-success' : 'bg-secondary'}`}>
          {isActive ? 'Active' : 'Inactive'}
        </span>
      </div>

      {!collapsed && (
        <div className="card-body">
          {/* Statistics Section */}
          <div className="section-header mb-2">Statistics</div>
          <div className="row g-2 mb-3">
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
                <div className="stat-label">Reveals</div>
              </div>
            </div>
          </div>

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
            <div className="row g-2 mb-3">
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
            <form onSubmit={handleTimingSave} className="mb-3">
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

          {/* Schedule Section */}
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Schedule</div>
            {!editingSchedule && (
              <button
                className="btn btn-sm btn-outline-primary"
                onClick={() => setEditingSchedule(true)}
                disabled={!canEdit}
                title={!canEdit ? 'Login required to edit' : ''}
              >
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editingSchedule ? (
            <div className="row g-2">
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Mode</div>
                  <div className="config-item-value">{schedule?.mode || 'all'}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Every Nth</div>
                  <div className="config-item-value">{schedule?.every_nth || 1}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Next N</div>
                  <div className="config-item-value">{schedule?.next_n || 0}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Start Slot</div>
                  <div className="config-item-value">{schedule?.start_slot || 0}</div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleScheduleSave}>
              <div className="mb-2">
                <label className="form-label">Mode</label>
                <select
                  className="form-select form-select-sm"
                  value={scheduleForm.mode}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, mode: e.target.value })}
                  required
                >
                  <option value="all">All</option>
                  <option value="every_nth">Every Nth</option>
                  <option value="next_n">Next N</option>
                </select>
              </div>
              <div className="mb-2">
                <label className="form-label">Every Nth</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={scheduleForm.every_nth}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, every_nth: parseInt(e.target.value) || 1 })}
                  min={1}
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Next N</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={scheduleForm.next_n}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, next_n: parseInt(e.target.value) || 0 })}
                  min={0}
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Start Slot</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={scheduleForm.start_slot}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, start_slot: parseInt(e.target.value) || 0 })}
                  min={0}
                />
              </div>
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditingSchedule(false)}>
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
