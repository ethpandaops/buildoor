import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { Config } from '../types';

interface BuilderConfigPanelProps {
  config: Config | null;
}

interface BuilderFormState {
  build_start_time: number;
  payload_build_delay: number;
}

export const BuilderConfigPanel: React.FC<BuilderConfigPanelProps> = ({ config }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [collapsed, setCollapsed] = useState(false);
  const [editing, setEditing] = useState(false);

  const [form, setForm] = useState<BuilderFormState>({
    build_start_time: 0,
    payload_build_delay: 0,
  });

  // Get payload_build_time from the root-level config (sent by SSE)
  const payloadBuildDelay = (() => {
    const rootValue = (config as unknown as Record<string, unknown>)?.payload_build_time;
    if (typeof rootValue === 'number') return rootValue;
    return config?.epbs?.payload_build_delay || 0;
  })();

  // Sync form state when not editing
  useEffect(() => {
    if (!editing && config?.epbs) {
      setForm({
        build_start_time: config.epbs.build_start_time || 0,
        payload_build_delay: payloadBuildDelay,
      });
    }
  }, [config, editing, payloadBuildDelay]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const authToken = getAuthHeader();
    if (!authToken) {
      alert('You must be logged in to update configuration');
      return;
    }
    try {
      const response = await fetch('/api/config/builder', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${authToken}`,
        },
        body: JSON.stringify(form),
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to update: ' + result.error);
      } else {
        setEditing(false);
      }
    } catch (err) {
      alert('Error: ' + err);
    }
  };

  const canEdit = isLoggedIn;

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} me-2`}></i>
        <h5 className="mb-0 me-2">Builder</h5>
      </div>

      {!collapsed && (
        <div className="card-body">
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Build Config</div>
            {!editing && (
              <button
                className="btn btn-sm btn-outline-primary"
                onClick={() => setEditing(true)}
                disabled={!canEdit}
                title={!canEdit ? 'Login required to edit' : ''}
              >
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editing ? (
            <div className="row g-2">
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Build Start</div>
                  <div className="config-item-value">{config?.epbs?.build_start_time || 0} ms</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Payload Build Delay</div>
                  <div className="config-item-value">{payloadBuildDelay} ms</div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleSave}>
              <div className="mb-2">
                <label className="form-label">Build Start Time (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={form.build_start_time}
                  onChange={(e) => setForm({ ...form, build_start_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Payload Build Delay (ms)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={form.payload_build_delay}
                  onChange={(e) => setForm({ ...form, payload_build_delay: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditing(false)}>
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
