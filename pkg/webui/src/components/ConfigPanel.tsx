import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { Config, EPBSConfig, ScheduleConfig } from '../types';

interface ConfigPanelProps {
  config: Config | null;
}

export const ConfigPanel: React.FC<ConfigPanelProps> = ({ config }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [editingEpbs, setEditingEpbs] = useState(false);
  const [editingSchedule, setEditingSchedule] = useState(false);
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

  // Only sync form state with config when NOT editing
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

  const canEdit = isLoggedIn;

  const epbs = config?.epbs;
  const schedule = config?.schedule;

  return (
    <>
      {/* EPBS Config */}
      <div className="card mb-3">
        <div className="card-header d-flex justify-content-between align-items-center">
          <h5 className="mb-0">EPBS Timing Config</h5>
          <button
            className="btn btn-sm btn-outline-primary"
            onClick={() => setEditingEpbs(!editingEpbs)}
            disabled={!canEdit}
            title={!canEdit ? 'Login required to edit' : ''}
          >
            <i className="fas fa-edit"></i>
          </button>
        </div>
        <div className="card-body">
          {!editingEpbs ? (
            <table className="table table-sm table-borderless mb-0">
              <tbody>
                <tr>
                  <td>Build Start:</td>
                  <td className="text-end">{epbs?.build_start_time || 0} ms</td>
                </tr>
                <tr>
                  <td>Bid Start:</td>
                  <td className="text-end">{epbs?.bid_start_time || 0} ms</td>
                </tr>
                <tr>
                  <td>Bid End:</td>
                  <td className="text-end">{epbs?.bid_end_time || 0} ms</td>
                </tr>
                <tr>
                  <td>Reveal Time:</td>
                  <td className="text-end">{epbs?.reveal_time || 0} ms</td>
                </tr>
                <tr>
                  <td>Bid Min Amount:</td>
                  <td className="text-end">{epbs?.bid_min_amount || 0} gwei</td>
                </tr>
                <tr>
                  <td>Bid Increase:</td>
                  <td className="text-end">{epbs?.bid_increase || 0} gwei</td>
                </tr>
                <tr>
                  <td>Bid Interval:</td>
                  <td className="text-end">{epbs?.bid_interval || 0} ms</td>
                </tr>
              </tbody>
            </table>
          ) : (
            <form onSubmit={handleEpbsSave}>
              <div className="mb-2">
                <label className="form-label">Build Start Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.build_start_time}
                  onChange={(e) => setEpbsForm({ ...epbsForm, build_start_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Start Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_start_time}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_start_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid End Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_end_time}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_end_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Reveal Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.reveal_time}
                  onChange={(e) => setEpbsForm({ ...epbsForm, reveal_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Min Amount (gwei)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_min_amount}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_min_amount: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Increase (gwei)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_increase}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_increase: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Interval (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={epbsForm.bid_interval}
                  onChange={(e) => setEpbsForm({ ...epbsForm, bid_interval: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditingEpbs(false)}>
                  Cancel
                </button>
              </div>
            </form>
          )}
        </div>
      </div>

      {/* Schedule Config */}
      <div className="card mb-3">
        <div className="card-header d-flex justify-content-between align-items-center">
          <h5 className="mb-0">Schedule Config</h5>
          <button
            className="btn btn-sm btn-outline-primary"
            onClick={() => setEditingSchedule(!editingSchedule)}
            disabled={!canEdit}
            title={!canEdit ? 'Login required to edit' : ''}
          >
            <i className="fas fa-edit"></i>
          </button>
        </div>
        <div className="card-body">
          {!editingSchedule ? (
            <table className="table table-sm table-borderless mb-0">
              <tbody>
                <tr>
                  <td>Mode:</td>
                  <td className="text-end">{schedule?.mode || 'all'}</td>
                </tr>
                <tr>
                  <td>Every Nth:</td>
                  <td className="text-end">{schedule?.every_nth || 1}</td>
                </tr>
                <tr>
                  <td>Next N:</td>
                  <td className="text-end">{schedule?.next_n || 0}</td>
                </tr>
              </tbody>
            </table>
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
    </>
  );
};
