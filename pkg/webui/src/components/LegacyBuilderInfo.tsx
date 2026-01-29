import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { LegacyBuilderInfo as LegacyBuilderInfoType, LegacyBuilderConfig, ServiceStatus } from '../types';

interface LegacyBuilderInfoProps {
  legacyBuilderInfo: LegacyBuilderInfoType | null;
  serviceStatus: ServiceStatus | null;
}

export const LegacyBuilderInfo: React.FC<LegacyBuilderInfoProps> = ({ legacyBuilderInfo, serviceStatus }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [editing, setEditing] = useState(false);
  const [toggling, setToggling] = useState(false);
  const [configData, setConfigData] = useState<LegacyBuilderConfig | null>(null);
  const [form, setForm] = useState<LegacyBuilderConfig>({
    build_start_time: -2000,
    submit_start_time: -500,
    submit_end_time: 4000,
    submit_interval: 0,
    payment_mode: 'fixed',
    fixed_payment: '10000000000000000',
    payment_percentage: 9000
  });

  useEffect(() => {
    if (!editing || configData) return;

    fetch('/api/legacy-builder/status')
      .then(res => res.json())
      .then(data => {
        if (data.payment_mode) {
          setForm(prev => ({
            ...prev,
            payment_mode: data.payment_mode || prev.payment_mode,
            fixed_payment: data.fixed_payment || prev.fixed_payment,
            payment_percentage: data.payment_percentage || prev.payment_percentage
          }));
        }
        setConfigData(data);
      })
      .catch(() => {});
  }, [editing, configData]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const authToken = getAuthHeader();
    if (!authToken) {
      alert('You must be logged in to update configuration');
      return;
    }

    try {
      const response = await fetch('/api/config/legacy-builder', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${authToken}`,
        },
        body: JSON.stringify(form)
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to update: ' + result.error);
      } else {
        setEditing(false);
        setConfigData(null);
      }
    } catch (err) {
      alert('Error: ' + err);
    }
  };

  const handleToggleLegacy = async () => {
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
        body: JSON.stringify({ legacy_enabled: !serviceStatus?.legacy_enabled })
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

  const legacyAvailable = serviceStatus?.legacy_available ?? false;
  const legacyEnabled = serviceStatus?.legacy_enabled ?? false;

  if (!legacyAvailable) {
    return null;
  }

  const canEdit = isLoggedIn;

  return (
    <div className="card mb-3">
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">Legacy PBS</h5>
        <div className="d-flex gap-2 align-items-center">
          <span className={`badge ${legacyEnabled ? 'bg-success' : 'bg-secondary'}`}>
            {legacyEnabled ? 'Active' : 'Inactive'}
          </span>
          {canEdit && (
            <button
              className={`btn btn-sm ${legacyEnabled ? 'btn-outline-danger' : 'btn-outline-success'}`}
              onClick={handleToggleLegacy}
              disabled={toggling}
              title={legacyEnabled ? 'Disable Legacy Builder' : 'Enable Legacy Builder'}
            >
              <i className={`fas ${legacyEnabled ? 'fa-pause' : 'fa-play'}`}></i>
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
              <div className="config-item-label">Relays</div>
              <div className="config-item-value">{legacyBuilderInfo?.relay_count ?? 0}</div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Validators</div>
              <div className="config-item-value">{legacyBuilderInfo?.validators_tracked ?? 0}</div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Submitted</div>
              <div className="config-item-value">{legacyBuilderInfo?.blocks_submitted ?? 0}</div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Accepted</div>
              <div className="config-item-value">{legacyBuilderInfo?.blocks_accepted ?? 0}</div>
            </div>
          </div>
          {(legacyBuilderInfo?.submission_failures ?? 0) > 0 && (
            <div className="col-6">
              <div className="config-item">
                <div className="config-item-label">Failures</div>
                <div className="config-item-value text-danger">{legacyBuilderInfo?.submission_failures ?? 0}</div>
              </div>
            </div>
          )}
        </div>

        {/* Configuration */}
        <div className="d-flex justify-content-between align-items-center mb-2">
          <div className="section-header">Configuration</div>
          <button
            className="btn btn-sm btn-outline-primary py-0 px-1"
            onClick={() => setEditing(!editing)}
            disabled={!canEdit}
            title={!canEdit ? 'Login required to edit' : ''}
          >
            <i className="fas fa-edit" style={{ fontSize: '11px' }}></i>
          </button>
        </div>
        {!editing ? (
          <div className="row g-2">
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Build Start</div>
                <div className="config-item-value">{form.build_start_time} ms</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Submit Start</div>
                <div className="config-item-value">{form.submit_start_time} ms</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Submit End</div>
                <div className="config-item-value">{form.submit_end_time} ms</div>
              </div>
            </div>
            <div className="col-12 col-sm-6">
              <div className="config-item">
                <div className="config-item-label">Payment Mode</div>
                <div className="config-item-value">{form.payment_mode}</div>
              </div>
            </div>
            {form.payment_mode === 'fixed' && (
              <div className="col-12 col-sm-6">
                <div className="config-item">
                  <div className="config-item-label">Fixed Payment</div>
                  <div className="config-item-value" style={{ fontSize: '12px' }}>{form.fixed_payment} wei</div>
                </div>
              </div>
            )}
            {form.payment_mode === 'percentage' && (
              <div className="col-12 col-sm-6">
                <div className="config-item">
                  <div className="config-item-label">Percentage</div>
                  <div className="config-item-value">{form.payment_percentage} bps</div>
                </div>
              </div>
            )}
          </div>
        ) : (
          <form onSubmit={handleSave}>
            <div className="row g-2 mb-2">
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Build Start (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={form.build_start_time}
                  onChange={(e) => setForm({ ...form, build_start_time: parseInt(e.target.value) || 0 })}
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Submit Start (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={form.submit_start_time}
                  onChange={(e) => setForm({ ...form, submit_start_time: parseInt(e.target.value) || 0 })}
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Submit End (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={form.submit_end_time}
                  onChange={(e) => setForm({ ...form, submit_end_time: parseInt(e.target.value) || 0 })}
                />
              </div>
              <div className="col-12 col-sm-6">
                <label className="form-label mb-0 small">Payment Mode</label>
                <select
                  className="form-select form-select-sm"
                  value={form.payment_mode}
                  onChange={(e) => setForm({ ...form, payment_mode: e.target.value })}
                >
                  <option value="fixed">Fixed</option>
                  <option value="percentage">Percentage</option>
                </select>
              </div>
              {form.payment_mode === 'fixed' && (
                <div className="col-12 col-sm-6">
                  <label className="form-label mb-0 small">Fixed Payment (wei)</label>
                  <input
                    type="text"
                    className="form-control form-control-sm"
                    value={form.fixed_payment}
                    onChange={(e) => setForm({ ...form, fixed_payment: e.target.value })}
                  />
                </div>
              )}
              {form.payment_mode === 'percentage' && (
                <div className="col-12 col-sm-6">
                  <label className="form-label mb-0 small">Percentage (bps, 10000=100%)</label>
                  <input
                    type="number"
                    className="form-control form-control-sm"
                    value={form.payment_percentage}
                    onChange={(e) => setForm({ ...form, payment_percentage: parseInt(e.target.value) || 0 })}
                    min={0}
                    max={10000}
                  />
                </div>
              )}
            </div>
            <div className="d-flex gap-2">
              <button type="submit" className="btn btn-sm btn-primary">Save</button>
              <button type="button" className="btn btn-sm btn-secondary" onClick={() => { setEditing(false); setConfigData(null); }}>
                Cancel
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
};
