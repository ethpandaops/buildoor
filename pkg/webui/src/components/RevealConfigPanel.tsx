import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { Config, RevealConfig } from '../types';

interface RevealConfigPanelProps {
  config: Config | null;
}

const GATE_MODE_LABELS: Record<string, string> = {
  time: 'Timed',
  vote: 'Vote threshold',
  vote_or_time: 'Vote threshold OR time',
  vote_and_time: 'Vote threshold AND time',
};

// Compact variants for the half-width settings grid.
const GATE_MODE_SHORT: Record<string, string> = {
  time: 'timed',
  vote: 'vote',
  vote_or_time: 'vote / time',
  vote_and_time: 'vote + time',
};

const BROADCAST_VALIDATION_LABELS: Record<string, string> = {
  gossip: 'gossip (lightweight checks)',
  consensus: 'consensus (full checks)',
  consensus_and_equivocation: 'consensus + equivocation',
};

const BROADCAST_VALIDATION_SHORT: Record<string, string> = {
  gossip: 'gossip',
  consensus: 'consensus',
  consensus_and_equivocation: 'cons. + equiv.',
};

// RevealConfigPanel is the standalone payload-reveal settings card: the
// reveal serves both flows (p2p bidder and Builder API), so its settings
// live outside the ePBS bidder section. Edits go through the generic
// path-based settings endpoint with reveal.* keys.
export const RevealConfigPanel: React.FC<RevealConfigPanelProps> = ({ config }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [collapsed, setCollapsed] = useState(true);
  const [editing, setEditing] = useState(false);
  const [toggling, setToggling] = useState(false);

  const reveal = config?.reveal;

  const [form, setForm] = useState<RevealConfig>({
    enabled: true,
    gate_mode: 'time',
    time_ms: 0,
    vote_threshold_pct: 60,
    broadcast_validation: 'gossip',
    max_attempts: 3,
    retry_interval_ms: 500,
  });

  useEffect(() => {
    if (!editing && reveal) {
      setForm({ ...reveal });
    }
  }, [reveal, editing]);

  const postSettings = async (settings: Record<string, unknown>): Promise<boolean> => {
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = await getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }
    try {
      const response = await fetch('/api/config/settings', {
        method: 'POST',
        headers,
        body: JSON.stringify(settings),
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to update: ' + result.error);
        return false;
      }
      return true;
    } catch (err) {
      alert('Error: ' + err);
      return false;
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const ok = await postSettings({
      'reveal.gate_mode': form.gate_mode,
      'reveal.time_ms': form.time_ms,
      'reveal.vote_threshold_pct': form.vote_threshold_pct,
      'reveal.broadcast_validation': form.broadcast_validation,
      'reveal.max_attempts': form.max_attempts,
      'reveal.retry_interval_ms': form.retry_interval_ms,
    });
    if (ok) setEditing(false);
  };

  const handleToggle = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!isLoggedIn || !reveal) return;
    setToggling(true);
    try {
      await postSettings({ 'reveal.enabled': !reveal.enabled });
    } finally {
      setToggling(false);
    }
  };

  const enabled = reveal?.enabled ?? true;
  const gateMode = reveal?.gate_mode ?? 'time';
  const timeGated = gateMode !== 'vote';
  const voteGated = gateMode !== 'time';

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} me-2`}></i>
        <h5 className="mb-0 me-2">Payload Reveal</h5>
        {enabled ? (
          <span className="badge bg-success">Active</span>
        ) : (
          <span className="badge bg-secondary">Disabled</span>
        )}
        {isLoggedIn && (
          <button
            className={`btn btn-sm ms-auto ${enabled ? 'btn-outline-danger' : 'btn-outline-success'}`}
            onClick={handleToggle}
            disabled={toggling || !reveal}
            title={enabled ? 'Disable payload reveals' : 'Enable payload reveals'}
          >
            <i className={`fas ${enabled ? 'fa-pause' : 'fa-play'}`}></i>
          </button>
        )}
      </div>

      {!collapsed && (
        <div className="card-body">
          {!enabled && (
            <div className="alert alert-warning small mb-2 py-1 px-2">
              <i className="fas fa-eye-slash me-1"></i>
              Reveals are globally disabled — won payloads are withheld
              (per-slot plans in custom mode still force their reveal).
            </div>
          )}
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Reveal Settings</div>
            {isLoggedIn && !editing && (
              <button className="btn btn-sm btn-outline-primary" onClick={() => setEditing(true)}>
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editing ? (
            <div className="row g-2">
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Gate</div>
                  <div className="config-item-value">{GATE_MODE_SHORT[gateMode] ?? gateMode}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Reveal Time</div>
                  <div className="config-item-value">
                    {timeGated ? `${reveal?.time_ms ?? 0} ms` : '—'}
                  </div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Vote Threshold</div>
                  <div className="config-item-value">
                    {voteGated ? `${reveal?.vote_threshold_pct ?? 0}%` : '—'}
                  </div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Broadcast Valid.</div>
                  <div className="config-item-value">
                    {BROADCAST_VALIDATION_SHORT[reveal?.broadcast_validation ?? 'gossip'] ??
                      reveal?.broadcast_validation}
                  </div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Max Attempts</div>
                  <div className="config-item-value">{reveal?.max_attempts ?? 0}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Retry Interval</div>
                  <div className="config-item-value">{reveal?.retry_interval_ms ?? 0} ms</div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleSave}>
              <div className="mb-2">
                <label className="form-label">Gate Mode</label>
                <select
                  className="form-select form-select-sm"
                  value={form.gate_mode}
                  onChange={(e) => setForm({ ...form, gate_mode: e.target.value })}
                >
                  {Object.entries(GATE_MODE_LABELS).map(([value, label]) => (
                    <option key={value} value={value}>{label}</option>
                  ))}
                </select>
                <div className="form-text">
                  When to reveal a won payload: at a fixed time, as soon as the
                  vote threshold on the committing block is reached, whichever
                  comes first, or only once both gates are open.
                </div>
              </div>
              <div className="mb-2">
                <label className="form-label">Reveal Time (ms into slot)</label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={form.time_ms}
                  disabled={form.gate_mode === 'vote'}
                  onChange={(e) => setForm({ ...form, time_ms: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Vote Threshold (%)</label>
                <input
                  type="number"
                  min={0}
                  max={100}
                  className="form-control form-control-sm"
                  value={form.vote_threshold_pct}
                  disabled={form.gate_mode === 'time'}
                  onChange={(e) => setForm({ ...form, vote_threshold_pct: parseInt(e.target.value) || 0 })}
                  required
                />
                <div className="form-text">
                  Head-vote participation on the committing block that opens
                  the vote gate. Unsatisfied gates withhold at slot end.
                </div>
              </div>
              <div className="mb-2">
                <label className="form-label">Broadcast Validation</label>
                <select
                  className="form-select form-select-sm"
                  value={form.broadcast_validation}
                  onChange={(e) => setForm({ ...form, broadcast_validation: e.target.value })}
                >
                  {Object.entries(BROADCAST_VALIDATION_LABELS).map(([value, label]) => (
                    <option key={value} value={value}>{label}</option>
                  ))}
                </select>
                <div className="form-text">
                  Validation the beacon node applies before broadcasting the
                  envelope. consensus + equivocation protects against
                  unbundling via equivocating blocks.
                </div>
              </div>
              <div className="mb-2">
                <label className="form-label">Max Attempts</label>
                <input
                  type="number"
                  min={1}
                  className="form-control form-control-sm"
                  value={form.max_attempts}
                  onChange={(e) => setForm({ ...form, max_attempts: parseInt(e.target.value) || 1 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Retry Interval (ms)</label>
                <input
                  type="number"
                  min={0}
                  className="form-control form-control-sm"
                  value={form.retry_interval_ms}
                  onChange={(e) => setForm({ ...form, retry_interval_ms: parseInt(e.target.value) || 0 })}
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
