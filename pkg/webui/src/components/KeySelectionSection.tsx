import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import { InfoHint } from './InfoHint';
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
  single: 'single (lowest-index key first)',
  random: 'random',
  least_used: 'least used',
};

// Setting explanations shared by the view rows and the edit form.
const HINT_BID_CANDIDATE = (
  <>
    Which built candidate payload the slot&apos;s p2p bids commit to
    (<code>epbs.bid_candidate</code>). A specific candidate only bids when that candidate was
    actually built (see Build Candidates). Per-slot action plans can override it.
    <dl>
      <dt>auto</dt>
      <dd>The payload matching the current head and its payload status (empty-parent build when the head&apos;s payload was withheld), else the slot&apos;s primary payload.</dd>
      <dt>all</dt>
      <dd>Bids every built candidate, spending keys per candidate: deliberate multi-parent gossip.</dd>
      <dt>parent (full)</dt>
      <dd>Head block and its payload.</dd>
      <dt>parent (empty payload)</dt>
      <dd>Head block on its execution parent (payload-miss case).</dd>
      <dt>grandparent (reorg)</dt>
      <dd>Head block&apos;s parent, reorging the head out.</dd>
      <dt>grandparent (empty payload)</dt>
      <dd>Reorg plus a withheld grandparent payload.</dd>
    </dl>
  </>
);

const HINT_CANDIDATE_SWITCH = (
  <>
    Only affects <em>auto</em> bid candidate selection (<code>epbs.bid_candidate_switch</code>).
    <dl>
      <dt>sticky per slot (off)</dt>
      <dd>Keeps bidding the first candidate chosen for the slot.</dd>
      <dt>mid-slot allowed (on)</dt>
      <dd>Re-selects on every bid and follows chain view changes; a switched bid rarely propagates past the gossip first-seen rule.</dd>
    </dl>
  </>
);

const HINT_KEY_STRATEGY = (
  <>
    Order the active builder keys are offered in for each bid (<code>epbs.key_strategy</code>);
    keys that already bid this slot are skipped. Keys that cannot cover the bid sort last but are
    still used when nothing else is left. Also used by Builder API bids unless the Builder API sets
    its own strategy. Per-slot action plans can override it.
    <dl>
      <dt>round robin</dt>
      <dd>Rotates the starting key by slot, spreading bids and payments evenly.</dd>
      <dt>single</dt>
      <dd>Lowest-index active key that has not bid this slot yet. Combine with Keys / Slot = 1 for single-key behaviour.</dd>
      <dt>random</dt>
      <dd>Shuffles the keys per slot (deterministic per slot).</dd>
      <dt>least used</dt>
      <dd>Prefers the keys with the fewest bids so far.</dd>
    </dl>
  </>
);

const HINT_KEYS_PER_SLOT = (
  <>
    Maximum distinct builder keys that may bid one slot (<code>epbs.bid_keys_per_slot</code>).
    Since a key bids at most once per slot, this also caps the slot&apos;s bid count. 0 = no cap
    beyond the active fleet; 1 = one gossiped bid per slot. Per-slot action plans can override it.
  </>
);

const HINT_KEYS_PER_STEP = (
  <>
    Keys that bid a payload per bid interval step, each one bid increase above the previous
    (<code>epbs.bid_keys_per_step</code>). 1 = walk the fleet up the price ladder one key per
    interval; 0 = spend every remaining key at once when the window opens. Per-slot action plans can
    override it.
  </>
);

interface KeySelectionForm {
  bid_candidate: string;
  bid_candidate_switch: boolean;
  key_strategy: string;
  bid_keys_per_slot: number;
  bid_keys_per_step: number;
}

const DEFAULT_FORM: KeySelectionForm = {
  bid_candidate: 'auto',
  bid_candidate_switch: false,
  key_strategy: 'round_robin',
  bid_keys_per_slot: 0,
  bid_keys_per_step: 1,
};

const formFromConfig = (epbs: EPBSConfig | undefined): KeySelectionForm => ({
  bid_candidate: epbs?.bid_candidate || DEFAULT_FORM.bid_candidate,
  bid_candidate_switch: epbs?.bid_candidate_switch ?? DEFAULT_FORM.bid_candidate_switch,
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
          'epbs.bid_candidate_switch': form.bid_candidate_switch,
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
              <div className="config-item-label">Bid Candidate <InfoHint>{HINT_BID_CANDIDATE}</InfoHint></div>
              <div className="config-item-value">
                {CANDIDATE_LABELS[epbs?.bid_candidate || 'auto'] ?? epbs?.bid_candidate}
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Candidate Switch <InfoHint alignEnd>{HINT_CANDIDATE_SWITCH}</InfoHint></div>
              <div className="config-item-value">
                {epbs?.bid_candidate_switch ? 'mid-slot allowed' : 'sticky per slot'}
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Key Strategy <InfoHint>{HINT_KEY_STRATEGY}</InfoHint></div>
              <div className="config-item-value">
                {STRATEGY_LABELS[epbs?.key_strategy || 'round_robin'] ?? epbs?.key_strategy}
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Keys / Slot <InfoHint alignEnd>{HINT_KEYS_PER_SLOT}</InfoHint></div>
              <div className="config-item-value">
                {epbs?.bid_keys_per_slot ? epbs.bid_keys_per_slot : 'whole fleet'}
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Keys / Step <InfoHint>{HINT_KEYS_PER_STEP}</InfoHint></div>
              <div className="config-item-value">
                {epbs?.bid_keys_per_step ? epbs.bid_keys_per_step : 'all remaining'}
              </div>
            </div>
          </div>
        </div>
      ) : (
        <form onSubmit={handleSave}>
          <div className="mb-2">
            <label className="form-label">Bid Candidate <InfoHint>{HINT_BID_CANDIDATE}</InfoHint></label>
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

          <div className="form-check mb-2">
            <input
              type="checkbox"
              className="form-check-input"
              id="bid-candidate-switch"
              checked={form.bid_candidate_switch}
              onChange={(e) => setForm({ ...form, bid_candidate_switch: e.target.checked })}
            />
            <label className="form-check-label" htmlFor="bid-candidate-switch">
              Allow switching candidate mid-slot
            </label>
            <InfoHint>{HINT_CANDIDATE_SWITCH}</InfoHint>
            <div className="form-text">
              Lets the auto selection move to a different candidate when the
              chain view changes during the slot. Off keeps the first gossiped
              candidate — a switched bid rarely propagates past the gossip
              first-seen rule anyway.
            </div>
          </div>

          <div className="mb-2">
            <label className="form-label">Key Strategy <InfoHint>{HINT_KEY_STRATEGY}</InfoHint></label>
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
            <label className="form-label">Keys per Slot <InfoHint>{HINT_KEYS_PER_SLOT}</InfoHint></label>
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
            <label className="form-label">Keys per Step <InfoHint>{HINT_KEYS_PER_STEP}</InfoHint></label>
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
