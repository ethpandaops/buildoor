import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { Config, EPBSConfig, ScheduleConfig, ServiceStatus, Stats as StatsType } from '../types';

interface ConfigPanelProps {
  config: Config | null;
  serviceStatus: ServiceStatus | null;
  stats: StatsType | null;
}

export const ConfigPanel: React.FC<ConfigPanelProps> = ({ config, serviceStatus, stats }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [editingEpbs, setEditingEpbs] = useState(false);
  const [editingSchedule, setEditingSchedule] = useState(false);
  const [toggling, setToggling] = useState(false);
  const [epbsForm, setEpbsForm] = useState<EPBSConfig>({
    build_start_time: 0,
    bid_start_time: 0,
    bid_end_time: 0,
    reveal_time: 0,
    bid_min_amount: 0,
    bid_increase: 0,
    bid_interval: 0
  });
  const [scheduleForm, setScheduleForm] = useState<ScheduleConfig>({
    mode: 'all',
    every_nth: 1,
    next_n: 0,
    start_slot: 0
  });

  useEffect(() => {
    if (!editingEpbs && config?.epbs) {
      setEpbsForm(config.epbs);
    }
  }, [config, editingEpbs]);

  useEffect(() => {
    if (!editingSchedule && config?.schedule) {
      setScheduleForm(config.schedule);
    }
  }, [config, editingSchedule]);

  const handleEpbsSave = async (e: React.FormEvent) => {
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
        body: JSON.stringify(epbsForm)
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to update: ' + result.error);
      } else {
        setEditingEpbs(false);
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
        body: JSON.stringify(scheduleForm)
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

  const handleToggleEpbs = async () => {
    const authToken = getAuthHeader();
    if (!authToken) {
      alert('You must be logged in to toggle services');
      return;
    }
    setToggling(true);
    try {
      const response = await fetch('/api/services/toggle', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${authToken}`,
        },
        body: JSON.stringify({ epbs_enabled: !serviceStatus?.epbs_enabled })
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to toggle: ' + result.error);
      }
    } catch (err) {
      alert('Error: ' + err);
    } finally {
      setToggling(false);
    }
  };

  const canEdit = isLoggedIn;
  const epbsAvailable = serviceStatus?.epbs_available ?? false;
  const epbsEnabled = serviceStatus?.epbs_enabled ?? false;
  const epbs = config?.epbs;
  const schedule = config?.schedule;

  if (!epbsAvailable) {
    return null;
  }

  return (
    <div className="card mb-3">
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">ePBS</h5>
        <div className="d-flex gap-2 align-items-center">
          <span className={`badge ${epbsEnabled ? 'bg-success' : 'bg-secondary'}`}>
            {epbsEnabled ? 'Active' : 'Inactive'}
          </span>
          {canEdit && (
            <button
              className={`btn btn-sm ${epbsEnabled ? 'btn-outline-danger' : 'btn-outline-success'}`}
              onClick={handleToggleEpbs}
              disabled={toggling}
              title={epbsEnabled ? 'Disable ePBS' : 'Enable ePBS'}
            >
              <i className={`fas ${epbsEnabled ? 'fa-pause' : 'fa-play'}`}></i>
            </button>
          )}
        </div>
      </div>
      <div className="card-body p-2">
        {/* Statistics */}
        <div className="section-header mb-2">Statistics</div>
        <div className="row g-2 mb-3">
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Slots Built</div>
              <div className="config-item-value">{stats?.slots_built || 0}</div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Bids Submitted</div>
              <div className="config-item-value">{stats?.bids_submitted || 0}</div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Bids Won</div>
              <div className="config-item-value">{stats?.bids_won || 0}</div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Reveals</div>
              <div className="config-item-value">{stats?.reveals_success || 0}</div>
            </div>
          </div>
        </div>

        {/* Timing Config */}
        <div className="d-flex justify-content-between align-items-center mb-2">
          <div className="section-header">Timing Config</div>
          <button
            className="btn btn-sm btn-outline-primary py-0 px-1"
            onClick={() => setEditingEpbs(!editingEpbs)}
            disabled={!canEdit}
            title={!canEdit ? 'Login required to edit' : ''}
          >
            <i className="fas fa-edit" style={{ fontSize: '11px' }}></i>
          </button>
        </div>
        {!editingEpbs ? (
          <div className="row g-2 mb-3">
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Build Start</div>
                <div className="config-item-value">{epbs?.build_start_time || 0} ms</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Bid Start</div>
                <div className="config-item-value">{epbs?.bid_start_time || 0} ms</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Bid End</div>
                <div className="config-item-value">{epbs?.bid_end_time || 0} ms</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Reveal Time</div>
                <div className="config-item-value">{epbs?.reveal_time || 0} ms</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Bid Min Amount</div>
                <div className="config-item-value">{epbs?.bid_min_amount || 0} gwei</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Bid Increase</div>
                <div className="config-item-value">{epbs?.bid_increase || 0} gwei</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Bid Interval</div>
                <div className="config-item-value">{epbs?.bid_interval || 0} ms</div>
              </div>
            </div>
          </div>
        ) : (
          <form onSubmit={handleEpbsSave}>
            <div className="row g-2 mb-2">
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Build Start (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.build_start_time}
                  onChange={(e) => setEpbsForm({ ...epbsForm, build_start_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Bid Start (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_start_time}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_start_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Bid End (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_end_time}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_end_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Reveal Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.reveal_time}
                  onChange={(e) => setEpbsForm({ ...epbsForm, reveal_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Bid Min Amount (gwei)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_min_amount}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_min_amount: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Bid Increase (gwei)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_increase}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_increase: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Bid Interval (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_interval}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_interval: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
            </div>
            <div className="d-flex gap-2 mb-3">
              <button type="submit" className="btn btn-sm btn-primary">Save</button>
              <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditingEpbs(false)}>
                Cancel
              </button>
            </div>
          </form>
        )}

        {/* Schedule Config */}
        <div className="d-flex justify-content-between align-items-center mb-2">
          <div className="section-header">Schedule</div>
          <button
            className="btn btn-sm btn-outline-primary py-0 px-1"
            onClick={() => setEditingSchedule(!editingSchedule)}
            disabled={!canEdit}
            title={!canEdit ? 'Login required to edit' : ''}
          >
            <i className="fas fa-edit" style={{ fontSize: '11px' }}></i>
          </button>
        </div>
        {!editingSchedule ? (
          <div className="row g-2">
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Mode</div>
                <div className="config-item-value">{schedule?.mode || 'all'}</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Every Nth</div>
                <div className="config-item-value">{schedule?.every_nth || 1}</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Next N</div>
                <div className="config-item-value">{schedule?.next_n || 0}</div>
              </div>
            </div>
          </div>
        ) : (
          <form onSubmit={handleScheduleSave}>
            <div className="row g-2 mb-2">
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Mode</label>
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
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Every Nth</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={scheduleForm.every_nth}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, every_nth: parseInt(e.target.value) || 1 })}
                  min={1}
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Next N</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={scheduleForm.next_n}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, next_n: parseInt(e.target.value) || 0 })}
                  min={0}
                />
              </div>
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
    </div>
  );
};
