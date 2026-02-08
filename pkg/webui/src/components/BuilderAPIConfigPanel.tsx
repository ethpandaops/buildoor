import React, { useState } from 'react';
import type { BuilderAPIStatus } from '../types';
import { useAuth } from '../hooks/useAuth';

interface BuilderAPIConfigPanelProps {
  status: BuilderAPIStatus | null;
  loading?: boolean;
}

function formatGwei(gwei: number): string {
  if (gwei === 0) return '0';
  return gwei.toLocaleString() + ' Gwei';
}

export const BuilderAPIConfigPanel: React.FC<BuilderAPIConfigPanelProps> = ({ status, loading }) => {
  const [collapsed, setCollapsed] = useState(false);
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formData, setFormData] = useState({
    use_proposer_fee_recipient: false,
    block_value_subsidy_gwei: 0
  });
  const { getAuthHeader } = useAuth();

  const startEditing = () => {
    if (status) {
      setFormData({
        use_proposer_fee_recipient: status.use_proposer_fee_recipient,
        block_value_subsidy_gwei: status.block_value_subsidy_gwei
      });
    }
    setEditing(true);
  };

  const cancelEditing = () => {
    setEditing(false);
  };

  const saveConfig = async () => {
    setSaving(true);
    try {
      const headers: HeadersInit = { 'Content-Type': 'application/json' };
      const token = getAuthHeader();
      if (token) {
        headers['Authorization'] = `Bearer ${token}`;
      }
      const response = await fetch('/api/config/builder-api', {
        method: 'POST',
        headers,
        body: JSON.stringify(formData)
      });
      if (!response.ok) {
        throw new Error(`Failed to update: ${response.statusText}`);
      }
      setEditing(false);
    } catch (err) {
      console.error('Failed to save builder API config:', err);
    } finally {
      setSaving(false);
    }
  };

  if (!status && !loading) {
    return null;
  }

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex justify-content-between align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <div className="d-flex align-items-center gap-2">
          <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} small`}></i>
          <h6 className="mb-0">Builder API</h6>
        </div>
        <div className="d-flex align-items-center gap-2">
          {loading ? (
            <span className="badge bg-secondary">Loading...</span>
          ) : status?.enabled ? (
            <span className="badge bg-success">Active</span>
          ) : (
            <span className="badge bg-secondary">Inactive</span>
          )}
        </div>
      </div>
      {!collapsed && (
        <div className="card-body p-2">
          {loading ? (
            <div className="text-muted text-center small">Loading...</div>
          ) : !status ? (
            <div className="text-muted text-center small">Status unavailable</div>
          ) : (
            <>
              {/* Info row */}
              <div className="row g-2 mb-2">
                {status.port > 0 && (
                  <div className="col-6">
                    <div className="config-item">
                      <div className="config-item-label">Port</div>
                      <div className="config-item-value">{status.port}</div>
                    </div>
                  </div>
                )}
                <div className="col-6">
                  <div className="config-item">
                    <div className="config-item-label">Validators</div>
                    <div className="config-item-value">{status.validator_count}</div>
                  </div>
                </div>
              </div>

              {/* Configuration */}
              {!editing ? (
                <>
                  <div className="d-flex justify-content-between align-items-center mb-1">
                    <div className="section-header">Configuration</div>
                    <button className="btn btn-sm btn-outline-secondary" onClick={(e) => { e.stopPropagation(); startEditing(); }}>
                      <i className="fas fa-edit"></i>
                    </button>
                  </div>
                  <div className="row g-2">
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Proposer Fee Recipient</div>
                        <div className="config-item-value">
                          {status.use_proposer_fee_recipient ? (
                            <span className="badge bg-success">Yes</span>
                          ) : (
                            <span className="badge bg-secondary">No</span>
                          )}
                        </div>
                      </div>
                    </div>
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Block Value Subsidy</div>
                        <div className="config-item-value">{formatGwei(status.block_value_subsidy_gwei)}</div>
                      </div>
                    </div>
                  </div>
                </>
              ) : (
                <>
                  <div className="section-header mb-1">Configuration</div>
                  <div className="mb-2">
                    <div className="form-check">
                      <input
                        className="form-check-input"
                        type="checkbox"
                        id="useProposerFeeRecipient"
                        checked={formData.use_proposer_fee_recipient}
                        onChange={(e) => setFormData({ ...formData, use_proposer_fee_recipient: e.target.checked })}
                      />
                      <label className="form-check-label small" htmlFor="useProposerFeeRecipient">
                        Use Proposer Fee Recipient
                      </label>
                    </div>
                  </div>
                  <div className="mb-2">
                    <label className="form-label small mb-0">Block Value Subsidy (Gwei)</label>
                    <input
                      type="number"
                      className="form-control form-control-sm"
                      value={formData.block_value_subsidy_gwei}
                      onChange={(e) => setFormData({ ...formData, block_value_subsidy_gwei: parseInt(e.target.value) || 0 })}
                    />
                  </div>
                  <div className="d-flex gap-2">
                    <button className="btn btn-sm btn-primary" onClick={saveConfig} disabled={saving}>
                      {saving ? 'Saving...' : 'Save'}
                    </button>
                    <button className="btn btn-sm btn-outline-secondary" onClick={cancelEditing}>
                      Cancel
                    </button>
                  </div>
                </>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
};
