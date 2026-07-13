import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { Config, EPBSConfig, ServiceStatus, SlotAction } from '../types';

interface ConfigPanelProps {
  config: Config | null;
  serviceStatus: ServiceStatus | null;
  currentSlot: number;
}

type EPBSFormState = EPBSConfig;

export const ConfigPanel: React.FC<ConfigPanelProps> = ({ config, serviceStatus, currentSlot }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [collapsed, setCollapsed] = useState(true);
  const [editingTiming, setEditingTiming] = useState(false);
  const [editingSlotActions, setEditingSlotActions] = useState(false);
  const [toggling, setToggling] = useState(false);
  const [savingSlotActions, setSavingSlotActions] = useState(false);
  const [slotActionSlots, setSlotActionSlots] = useState<number[]>([]);
  const [newSlot, setNewSlot] = useState('');
  const [slotActionError, setSlotActionError] = useState('');
  const [displayedSlotActions, setDisplayedSlotActions] = useState<Record<string, SlotAction>>({});

  const [timingForm, setTimingForm] = useState<EPBSFormState>({
    build_start_time: 0,
    bid_start_time: 0,
    bid_end_time: 0,
    reveal_time: 0,
    bid_min_amount: 0,
    bid_increase: 0,
    bid_interval: 0,
    bid_subsidy: 0,
  });

  // Sync timing form state when not editing
  useEffect(() => {
    if (!editingTiming && config?.epbs) {
      setTimingForm({ ...config.epbs });
    }
  }, [config, editingTiming]);

  useEffect(() => {
    setDisplayedSlotActions(config?.epbs?.slot_actions ?? {});
  }, [config]);

  // Only future entries are editable. If a configured slot starts while the
  // form is open, the backend keeps it immutable and the next save omits it.
  useEffect(() => {
    if (!editingSlotActions) {
      const futureSlots = Object.keys(displayedSlotActions)
        .map(Number)
        .filter((slot) => Number.isSafeInteger(slot) && slot > currentSlot)
        .sort((a, b) => a - b);
      setSlotActionSlots(futureSlots);
    }
  }, [displayedSlotActions, currentSlot, editingSlotActions]);

  const handleTimingSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }
    try {
      const response = await fetch('/api/config/epbs', {
        method: 'POST',
        headers,
        body: JSON.stringify({
          bid_start_time: timingForm.bid_start_time,
          bid_end_time: timingForm.bid_end_time,
          reveal_time: timingForm.reveal_time,
          bid_min_amount: timingForm.bid_min_amount,
          bid_increase: timingForm.bid_increase,
          bid_interval: timingForm.bid_interval,
          bid_subsidy: timingForm.bid_subsidy,
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
  const configuredSlots = Object.keys(displayedSlotActions)
    .map(Number)
    .filter(Number.isSafeInteger)
    .sort((a, b) => a - b);
  const lockedSlots = configuredSlots.filter((slot) => slot <= currentSlot);
  const isActive = serviceStatus?.epbs_enabled ?? false;
  const isAvailable = serviceStatus?.epbs_available ?? false;
  const registrationState = serviceStatus?.epbs_registration_state ?? 'unknown';
  const isRegistered = registrationState === 'registered';

  const handleToggle = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!isLoggedIn) return;
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = getAuthHeader();
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

  const addSlotAction = () => {
    const slot = Number(newSlot);
    if (!Number.isSafeInteger(slot) || slot < 0) {
      setSlotActionError('Slot must be a non-negative whole number.');
      return;
    }
    if (slot <= currentSlot) {
      setSlotActionError(`Slot must be in the future (current slot ${currentSlot}).`);
      return;
    }

    setSlotActionSlots((slots) => [...new Set([...slots, slot])].sort((a, b) => a - b));
    setNewSlot('');
    setSlotActionError('');
  };

  const postSlotActions = async (slots: number[]) => {
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }

    const slotActions = Object.fromEntries(
      slots
        .filter((slot) => slot > currentSlot)
        .map((slot) => [String(slot), { reveal: 'withhold' as const }])
    );

    setSavingSlotActions(true);
    setSlotActionError('');
    try {
      const response = await fetch('/api/config/epbs', {
        method: 'POST',
        headers,
        body: JSON.stringify({ slot_actions: slotActions }),
      });
      const result = await response.json();
      if (!response.ok || result.error) {
        throw new Error(result.error || `request failed with status ${response.status}`);
      }

      setDisplayedSlotActions(result.slot_actions ?? {});
      setEditingSlotActions(false);
      setNewSlot('');
    } catch (err) {
      setSlotActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setSavingSlotActions(false);
    }
  };

  const handleSlotActionsSave = async (e: React.FormEvent) => {
    e.preventDefault();
    await postSlotActions(slotActionSlots);
  };

  const handleClearSlotActions = async () => {
    await postSlotActions([]);
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
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid Subsidy</div>
                  <div className="config-item-value">{epbs?.bid_subsidy || 0} gwei</div>
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
              <div className="mb-2">
                <label className="form-label">Bid Subsidy (gwei)</label>
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
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditingTiming(false)}>
                  Cancel
                </button>
              </div>
            </form>
          )}

          <hr className="my-3" />

          {/* Exact-slot reveal fault actions */}
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Exact-slot Reveal Actions</div>
            {canEdit && isAvailable && !editingSlotActions && (
              <button
                className="btn btn-sm btn-outline-primary"
                onClick={() => {
                  setSlotActionError('');
                  setEditingSlotActions(true);
                }}
                title="Configure exact slots that should withhold their payload reveal"
              >
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editingSlotActions ? (
            <div>
              {configuredSlots.length === 0 ? (
                <div className="text-muted small">No reveal withholding actions configured.</div>
              ) : (
                <div className="d-flex flex-column gap-2">
                  {configuredSlots.map((slot) => (
                    <div key={slot} className="config-item d-flex justify-content-between align-items-center">
                      <div>
                        <div className="config-item-label">Slot {slot}</div>
                        <div className="config-item-value">Withhold payload reveal</div>
                      </div>
                      <span className={`badge ${slot <= currentSlot ? 'bg-secondary' : 'bg-warning text-dark'}`}>
                        {slot <= currentSlot ? 'Locked' : 'Pending'}
                      </span>
                    </div>
                  ))}
                </div>
              )}
              <div className="form-text mt-2">
                Buildoor still builds and bids normally. If it wins a configured slot, it retains the payload and does not publish the envelope.
              </div>
            </div>
          ) : (
            <form onSubmit={handleSlotActionsSave}>
              {lockedSlots.length > 0 && (
                <div className="alert alert-secondary small py-1 px-2">
                  Slot{lockedSlots.length > 1 ? 's' : ''} {lockedSlots.join(', ')} already started and will remain locked.
                </div>
              )}

              <div className="input-group input-group-sm mb-2">
                <span className="input-group-text">Slot</span>
                <input
                  type="number"
                  className="form-control"
                  value={newSlot}
                  min={Math.max(0, currentSlot + 1)}
                  max={Number.MAX_SAFE_INTEGER}
                  step={1}
                  placeholder={`>${currentSlot}`}
                  onChange={(e) => setNewSlot(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      addSlotAction();
                    }
                  }}
                />
                <button type="button" className="btn btn-outline-primary" onClick={addSlotAction} disabled={!newSlot}>
                  <i className="fas fa-plus me-1"></i>Add
                </button>
              </div>

              {slotActionSlots.length === 0 ? (
                <div className="text-muted small mb-2">No pending future actions. Saving will clear the pending set.</div>
              ) : (
                <div className="list-group mb-2">
                  {slotActionSlots.map((slot) => (
                    <div key={slot} className="list-group-item py-1 px-2 d-flex justify-content-between align-items-center">
                      <span><strong>Slot {slot}</strong> — withhold reveal</span>
                      <button
                        type="button"
                        className="btn btn-sm btn-outline-danger border-0"
                        onClick={() => setSlotActionSlots((slots) => slots.filter((item) => item !== slot))}
                        title={`Remove slot ${slot}`}
                      >
                        <i className="fas fa-times"></i>
                      </button>
                    </div>
                  ))}
                </div>
              )}

              {slotActionError && (
                <div className="alert alert-danger small py-1 px-2 mb-2">{slotActionError}</div>
              )}

              <div className="form-text mb-2">
                Saving replaces the complete pending future set. Actions become immutable when their slot starts.
              </div>
              <div className="d-flex flex-wrap gap-2">
                <button type="submit" className="btn btn-sm btn-primary" disabled={savingSlotActions}>
                  {savingSlotActions ? 'Saving…' : 'Save actions'}
                </button>
                <button
                  type="button"
                  className="btn btn-sm btn-outline-danger"
                  onClick={handleClearSlotActions}
                  disabled={savingSlotActions || slotActionSlots.length === 0}
                >
                  Clear pending
                </button>
                <button
                  type="button"
                  className="btn btn-sm btn-secondary"
                  onClick={() => {
                    setEditingSlotActions(false);
                    setSlotActionError('');
                    setNewSlot('');
                  }}
                  disabled={savingSlotActions}
                >
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
