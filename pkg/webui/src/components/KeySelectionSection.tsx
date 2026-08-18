import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { Config, EPBSConfig } from '../types';

interface KeySelectionSectionProps {
  config: Config | null;
}

// Which built candidate payload the slot's p2p bids commit to. "all" gossips a
// bid for every built candidate, so one slot can carry bids on several parents.
const CANDIDATE_LABELS: Record<string, string> = {
  auto: 'auto (match the chain view)',
  all: 'all built candidates',
  parent_full: 'parent (full)',
  parent_empty: 'parent (empty payload)',
  grandparent_full: 'grandparent (reorg)',
  grandparent_empty: 'grandparent (empty payload)',
};

// Order the fleet is walked in when picking the key for a bid.
const STRATEGY_LABELS: Record<string, string> = {
  round_robin: 'round robin',
  single: 'single (primary key only)',
  random: 'random',
  least_used: 'least used',
};

interface KeySelectionForm {
  bid_candidate: string;
  key_strategy: string;
  bid_keys_per_slot: number;
  bid_keys_per_step: number;
}

const DEFAULT_FORM: KeySelectionForm = {
  bid_candidate: 'auto',
  key_strategy: 'round_robin',
  bid_keys_per_slot: 0,
  bid_keys_per_step: 1,
};

const formFromConfig = (epbs: EPBSConfig | undefined): KeySelectionForm => ({
  bid_candidate: epbs?.bid_candidate || DEFAULT_FORM.bid_candidate,
  key_strategy: epbs?.key_strategy || DEFAULT_FORM.key_strategy,
  bid_keys_per_slot: epbs?.bid_keys_per_slot ?? DEFAULT_FORM.bid_keys_per_slot,
  bid_keys_per_step: epbs?.bid_keys_per_step ?? DEFAULT_FORM.bid_keys_per_step,
});

// KeySelectionSection is the global bid key selection policy, rendered as a
// section of the ePBS Bidder card: which candidate payloads the slot bids on
// and how many of the managed keys are spent doing it. Per-slot overrides live
// in the action plan; edits here go through the generic path-based settings
// endpoint with epbs.* keys.
export const KeySelectionSection: React.FC<KeySelectionSectionProps> = ({ config }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [editing, setEditing] = useState(false);

  const epbs = config?.epbs;

  const [form, setForm] = useState<KeySelectionForm>(DEFAULT_FORM);

  useEffect(() => {
    if (!editing) {
      setForm(formFromConfig(epbs));
    }
  }, [epbs, editing]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();

    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = await getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }

    try {
      const response = await fetch('/api/config/settings', {
        method: 'POST',
        headers,
        body: JSON.stringify({
          'epbs.bid_candidate': form.bid_candidate,
          'epbs.key_strategy': form.key_strategy,
          'epbs.bid_keys_per_slot': form.bid_keys_per_slot,
          'epbs.bid_keys_per_step': form.bid_keys_per_step,
        }),
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to update: ' + result.error);
        return;
      }
      setEditing(false);
    } catch (err) {
      alert('Error: ' + err);
    }
  };

  return (
    <>
      <div className="d-flex justify-content-between align-items-center mb-2 mt-3">
        <div className="section-header">Key Selection</div>
        {isLoggedIn && !editing && (
          <button className="btn btn-sm btn-outline-primary" onClick={() => setEditing(true)}>
            <i className="fas fa-pencil-alt"></i>
          </button>
        )}
      </div>
      <div className="form-text mt-0 mb-2">
        Gossip ignores every bid a builder makes after its first for a slot, so
        each bid spends a managed key that has not bid yet. These settings decide
        how many of the fleet a slot spends, and on which candidate payloads.
      </div>

      {!editing ? (
        <div className="row g-2">
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Bid Candidate</div>
              <div className="config-item-value">
                {CANDIDATE_LABELS[epbs?.bid_candidate || 'auto'] ?? epbs?.bid_candidate}
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Key Strategy</div>
              <div className="config-item-value">
                {STRATEGY_LABELS[epbs?.key_strategy || 'round_robin'] ?? epbs?.key_strategy}
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Keys / Slot</div>
              <div className="config-item-value">
                {epbs?.bid_keys_per_slot ? epbs.bid_keys_per_slot : 'whole fleet'}
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Keys / Step</div>
              <div className="config-item-value">
                {epbs?.bid_keys_per_step ? epbs.bid_keys_per_step : 'all remaining'}
              </div>
            </div>
          </div>
        </div>
      ) : (
        <form onSubmit={handleSave}>
          <div className="mb-2">
            <label className="form-label">Bid Candidate</label>
            <select
              className="form-select form-select-sm"
              value={form.bid_candidate}
              onChange={(e) => setForm({ ...form, bid_candidate: e.target.value })}
            >
              {Object.entries(CANDIDATE_LABELS).map(([value, label]) => (
                <option key={value} value={value}>{label}</option>
              ))}
            </select>
            <div className="form-text">
              Which built payload the bids commit to. <strong>all</strong> bids
              every candidate the slot built, spending keys per candidate — a
              deliberate multi-parent gossip scenario, since only one of those
              parents can end up canonical.
            </div>
          </div>

          <div className="mb-2">
            <label className="form-label">Key Strategy</label>
            <select
              className="form-select form-select-sm"
              value={form.key_strategy}
              onChange={(e) => setForm({ ...form, key_strategy: e.target.value })}
            >
              {Object.entries(STRATEGY_LABELS).map(([value, label]) => (
                <option key={value} value={value}>{label}</option>
              ))}
            </select>
            <div className="form-text">
              Order the active keys are offered in. Balance is a preference, not
              a filter: an underfunded key sorts last but is still used when
              nothing else can cover the bid.
            </div>
          </div>

          <div className="mb-2">
            <label className="form-label">Keys per Slot</label>
            <input
              type="number"
              min={0}
              className="form-control form-control-sm"
              value={form.bid_keys_per_slot}
              onChange={(e) =>
                setForm({ ...form, bid_keys_per_slot: parseInt(e.target.value) || 0 })
              }
              required
            />
            <div className="form-text">
              Caps how many distinct keys one slot may spend. 0 means every
              active key is available.
            </div>
          </div>

          <div className="mb-2">
            <label className="form-label">Keys per Step</label>
            <input
              type="number"
              min={0}
              className="form-control form-control-sm"
              value={form.bid_keys_per_step}
              onChange={(e) =>
                setForm({ ...form, bid_keys_per_step: parseInt(e.target.value) || 0 })
              }
              required
            />
            <div className="form-text">
              Keys spent on a payload per bid interval, each bidding one increase
              above the last. 1 walks the fleet up the price ladder; 0 spends
              every remaining key in a single step.
            </div>
          </div>

          <div className="d-flex gap-2">
            <button type="submit" className="btn btn-sm btn-primary">Save</button>
            <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </>
  );
};
