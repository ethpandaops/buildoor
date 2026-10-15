import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import { KeySelectionSection } from './KeySelectionSection';
import { useSettings } from '../hooks/useSettings';
import { InfoHint } from './InfoHint';
import type { Config, EPBSConfig, ServiceStatus } from '../types';

// Setting explanations shared by the view rows and the edit form.
const HINT_ENABLED = (
  <>
    Global switch for p2p bidding via gossip (<code>epbs_enabled</code>). Bidding also needs the
    Gloas fork and at least one active builder key. Per-slot action plans override it either way:
    a <em>custom</em> bid plan bids even while this is off, a <em>disabled</em> one suppresses a
    slot while it is on.
  </>
);

const HINT_BID_START = (
  <>
    Opening of the bid window in ms relative to the proposal slot&apos;s start
    (<code>epbs.bid_start_time</code>). Negative = before the slot starts. Startup default:
    -400 ms at 12 s slots, scaled to the slot time. Per-slot action plans can override the window.
  </>
);

const HINT_BID_END = (
  <>
    Close of the bid window in ms relative to the proposal slot&apos;s start
    (<code>epbs.bid_end_time</code>, exclusive). Bidding also stops as soon as the slot&apos;s
    block is seen. Startup default: -100 ms at 12 s slots, scaled to the slot time.
  </>
);

const HINT_BID_MIN = (
  <>
    Floor for the bid base in gwei (<code>epbs.bid_min_amount</code>): the first bid is
    max(block value, bid min) + subsidy. Ignored while a bid value override is set.
  </>
);

const HINT_BID_INCREASE = (
  <>
    Gwei added per re-bid of the same payload (<code>epbs.bid_increase</code>): the n-th bid is
    the base + (n-1) &times; increase. Also applies on top of a bid value override.
  </>
);

const HINT_BID_INTERVAL = (
  <>
    Minimum ms between two bids on the same payload inside the bid window
    (<code>epbs.bid_interval</code>). 0 = bid each payload only once. Each re-bid needs a key that
    has not bid this slot yet (see Key Selection).
  </>
);

const HINT_BID_SUBSIDY = (
  <>
    Flat gwei added to every bid (<code>epbs.bid_subsidy</code>) so it clears the proposer&apos;s
    local-EL threshold; otherwise the beacon node self-builds when its local block is worth more.
    0 = bid the real block value. Ignored while a bid value override is set.
  </>
);

const HINT_BID_VALUE_OVERRIDE = (
  <>
    Absolute bid base in gwei (<code>epbs.bid_value_override</code>) replacing max(block value,
    bid min) + subsidy, so it can underbid the block value. Bid increase still applies per re-bid.
    0 = off. A per-slot plan value wins over it.
  </>
);

const HINT_VOTE_MARKER = (
  <>
    Head-vote participation in percent marked as reached in the slot graph
    (<code>epbs.head_vote_threshold_pct</code>). Default 60 = the Gloas builder payment quorum.
    Display only: the reveal vote gate uses its own threshold under Payload Reveal. 0 = off.
  </>
);

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
  const { postSettings } = useSettings();

  const [timingForm, setTimingForm] = useState<EPBSFormState>({
    build_start_time: 0,
    bid_start_time: 0,
    bid_end_time: 0,
    bid_min_amount: 0,
    bid_increase: 0,
    bid_interval: 0,
    bid_subsidy: 0,
    bid_value_override: 0,
    head_vote_threshold_pct: 0,
  });

  // Sync timing form state when not editing
  useEffect(() => {
    if (!editingTiming && config?.epbs) {
      setTimingForm({ ...config.epbs });
    }
  }, [config, editingTiming]);

  const handleTimingSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const result = await postSettings({
      'epbs.bid_start_time': timingForm.bid_start_time,
      'epbs.bid_end_time': timingForm.bid_end_time,
      'epbs.bid_min_amount': timingForm.bid_min_amount,
      'epbs.bid_increase': timingForm.bid_increase,
      'epbs.bid_interval': timingForm.bid_interval,
      'epbs.bid_subsidy': timingForm.bid_subsidy,
      'epbs.bid_value_override': timingForm.bid_value_override ?? 0,
      'epbs.head_vote_threshold_pct': timingForm.head_vote_threshold_pct ?? 0,
    });
    if (result.ok) {
      setEditingTiming(false);
    } else {
      alert('Failed to update: ' + result.error);
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
    if (!isLoggedIn) return;
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = await getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }
    setToggling(true);
    try {
      await fetch('/api/services/toggle', {
        method: 'POST',
        headers,
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
        <span className="ms-1" onClick={(e) => e.stopPropagation()}>
          <InfoHint>{HINT_ENABLED}</InfoHint>
        </span>
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
            <div className="section-header">Bid Config</div>
            {canEdit && !editingTiming && (
              <button
                className="btn btn-sm btn-outline-primary"
                onClick={() => setEditingTiming(true)}
              >
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editingTiming ? (
            <div className="row g-2">
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Start <InfoHint>{HINT_BID_START}</InfoHint></div>
                  <div className="config-item-value">{epbs?.bid_start_time || 0} ms</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid End <InfoHint alignEnd>{HINT_BID_END}</InfoHint></div>
                  <div className="config-item-value">{epbs?.bid_end_time || 0} ms</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Min <InfoHint>{HINT_BID_MIN}</InfoHint></div>
                  <div className="config-item-value">{epbs?.bid_min_amount || 0} gwei</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Increase <InfoHint alignEnd>{HINT_BID_INCREASE}</InfoHint></div>
                  <div className="config-item-value">{epbs?.bid_increase || 0} gwei</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Interval <InfoHint>{HINT_BID_INTERVAL}</InfoHint></div>
                  <div className="config-item-value">{epbs?.bid_interval || 0} ms</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Subsidy <InfoHint alignEnd>{HINT_BID_SUBSIDY}</InfoHint></div>
                  <div className="config-item-value">{epbs?.bid_subsidy || 0} gwei</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Value Override <InfoHint>{HINT_BID_VALUE_OVERRIDE}</InfoHint></div>
                  <div className="config-item-value">
                    {epbs?.bid_value_override ? `${epbs.bid_value_override} gwei` : 'off'}
                  </div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Vote Marker <InfoHint alignEnd>{HINT_VOTE_MARKER}</InfoHint></div>
                  <div className="config-item-value">
                    {epbs?.head_vote_threshold_pct ? `${epbs.head_vote_threshold_pct}%` : 'off'}
                  </div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleTimingSave}>
              <div className="mb-2">
                <label className="form-label">Bid Start Time (ms) <InfoHint>{HINT_BID_START}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_start_time}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_start_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid End Time (ms) <InfoHint>{HINT_BID_END}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_end_time}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_end_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Min Amount (gwei) <InfoHint>{HINT_BID_MIN}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_min_amount}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_min_amount: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Increase (gwei) <InfoHint>{HINT_BID_INCREASE}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_increase}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_increase: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Interval (ms) <InfoHint>{HINT_BID_INTERVAL}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_interval}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_interval: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Subsidy (gwei) <InfoHint>{HINT_BID_SUBSIDY}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={timingForm.bid_subsidy}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_subsidy: parseInt(e.target.value) || 0 })}
                  required
                />
                <div className="form-text">
                  Flat gwei added to every bid so it clears the proposer's local-EL
                  threshold. Set to 0 to bid the real block value.
                </div>
              </div>
              <div className="mb-2">
                <label className="form-label">Bid Value Override (gwei) <InfoHint>{HINT_BID_VALUE_OVERRIDE}</InfoHint></label>
                <input
                  type="number"
                  min={0}
                  className="form-control form-control-sm"
                  value={timingForm.bid_value_override ?? 0}
                  onChange={(e) => setTimingForm({ ...timingForm, bid_value_override: parseInt(e.target.value) || 0 })}
                  required
                />
                <div className="form-text">
                  Absolute bid base replacing max(block value, bid min) + subsidy —
                  allows underbidding the block value. Bid Increase still applies
                  per re-bid. 0 = off.
                </div>
              </div>
              <div className="mb-2">
                <label className="form-label">Vote Marker Threshold (%) <InfoHint>{HINT_VOTE_MARKER}</InfoHint></label>
                <input
                  type="number"
                  min={0}
                  max={100}
                  className="form-control form-control-sm"
                  value={timingForm.head_vote_threshold_pct ?? 0}
                  onChange={(e) => setTimingForm({ ...timingForm, head_vote_threshold_pct: parseInt(e.target.value) || 0 })}
                  required
                />
                <div className="form-text">
                  Head-vote participation marked as reached in the slot graph.
                  Display only — the reveal vote gate has its own threshold under
                  Payload Reveal. 0 = off.
                </div>
              </div>
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditingTiming(false)}>
                  Cancel
                </button>
              </div>
            </form>
          )}

          <KeySelectionSection config={config} />
        </div>
      )}
    </div>
  );
};
