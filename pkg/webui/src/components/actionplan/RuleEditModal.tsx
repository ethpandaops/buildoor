import React, { useState } from 'react';
import type { BidPlan, BuilderAPIPlan, RevealPlan, SlotRule } from '../../types';
import { TransformEditor, type TransformState } from './TransformEditor';
import {
  BID_FIELDS,
  BUILDER_API_FIELDS,
  REVEAL_FIELDS,
  BuildForm,
  CategoryForm,
  initCategoryState,
  resolveCategory,
  type BuildFlagMode,
  type CategoryFormState,
} from './planForms';

// Ready-made scenarios; picking one fills the form, which stays editable.
interface Preset {
  id: string;
  label: string;
  hint: string;
  apply: (slotsPerEpoch: number) => Partial<SlotRule> & { slots_in_epoch: number[] };
}

const PRESETS: Preset[] = [
  {
    id: 'win-and-withhold',
    label: 'Win the last slot of every epoch, withhold the payload',
    hint: 'Outbids the other builders with a large subsidy on top of the block value, then never reveals the envelope — the block exists, its execution payload does not.',
    apply: (slotsPerEpoch) => ({
      id: 'slot-last-withhold',
      description: 'win the last slot of every epoch and withhold the payload',
      slots_in_epoch: [Math.max(0, slotsPerEpoch - 1)],
      // A subsidy adds to the block value, so it outbids competitors whatever
      // the block is worth — an absolute bid_value_gwei would have to be
      // re-tuned per network and silently loses when it guesses too low.
      bid: { mode: 'custom', bid_subsidy: 1000000000, ignore_missing_prefs: true },
      reveal: { mode: 'disabled' },
    }),
  },
  {
    id: 'withhold-only',
    label: 'Withhold the payload of the last slot of every epoch',
    hint: 'Leaves bidding on the global config and only suppresses the reveal when the slot is won.',
    apply: (slotsPerEpoch) => ({
      id: 'slot-last-no-reveal',
      description: 'withhold the payload of the last slot of every epoch',
      slots_in_epoch: [Math.max(0, slotsPerEpoch - 1)],
      reveal: { mode: 'disabled' },
    }),
  },
];

// parseSlotList reads a "31" / "0,15,31" / "28-31" slot-index list.
function parseSlotList(raw: string, slotsPerEpoch: number): { slots: number[]; error: string | null } {
  const slots: number[] = [];

  for (const part of raw.split(',')) {
    const token = part.trim();
    if (token === '') continue;

    const range = token.match(/^(\d+)\s*-\s*(\d+)$/);
    const bounds = range ? [Number(range[1]), Number(range[2])] : [Number(token), Number(token)];

    if (!bounds.every((n) => Number.isInteger(n) && n >= 0)) {
      return { slots, error: `"${token}" is not a slot index or range` };
    }
    if (bounds[1] < bounds[0]) {
      return { slots, error: `invalid range "${token}"` };
    }
    if (slotsPerEpoch > 0 && bounds[1] >= slotsPerEpoch) {
      return { slots, error: `slot index ${bounds[1]} is out of range (0–${slotsPerEpoch - 1})` };
    }

    for (let s = bounds[0]; s <= bounds[1]; s++) {
      if (!slots.includes(s)) slots.push(s);
    }
  }

  if (slots.length === 0) return { slots, error: 'at least one slot index is required' };

  return { slots: slots.sort((a, b) => a - b), error: null };
}

function parseOptionalEpoch(raw: string, label: string): { value?: number; error: string | null } {
  const token = raw.trim();
  if (token === '') return { error: null };

  const value = Number(token);
  if (!Number.isInteger(value) || value < 0) {
    return { error: `${label} must be a non-negative integer` };
  }

  return { value, error: null };
}

interface RuleEditModalProps {
  /** The rule being edited, or null for a new one. */
  rule: SlotRule | null;
  /** Ids already in use — a new rule must not silently replace one of them. */
  existingIds: string[];
  slotsPerEpoch: number;
  canEdit: boolean;
  saving: boolean;
  error: string | null;
  onSave: (rule: SlotRule) => void;
  onClose: () => void;
}

export const RuleEditModal: React.FC<RuleEditModalProps> = ({
  rule,
  existingIds,
  slotsPerEpoch,
  canEdit,
  saving,
  error,
  onSave,
  onClose,
}) => {
  const [id, setId] = useState(rule?.id ?? '');
  const [description, setDescription] = useState(rule?.description ?? '');
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [slotList, setSlotList] = useState((rule?.slots_in_epoch ?? []).join(','));
  const [fromEpoch, setFromEpoch] = useState(rule?.from_epoch?.toString() ?? '');
  const [toEpoch, setToEpoch] = useState(rule?.to_epoch?.toString() ?? '');

  const [bidState, setBidState] = useState<CategoryFormState>(() =>
    initCategoryState(rule?.bid, BID_FIELDS, false)
  );
  const [apiState, setApiState] = useState<CategoryFormState>(() =>
    initCategoryState(rule?.builder_api, BUILDER_API_FIELDS, false)
  );
  const [revealState, setRevealState] = useState<CategoryFormState>(() =>
    initCategoryState(rule?.reveal, REVEAL_FIELDS, false)
  );
  const [buildReorg, setBuildReorg] = useState<BuildFlagMode>(
    rule?.build?.reorg_parent_payload ? 'on' : 'off'
  );
  const [transforms, setTransforms] = useState<TransformState>({
    payload: rule?.transforms?.payload ?? '',
    bid: rule?.transforms?.bid ?? '',
    envelope: rule?.transforms?.envelope ?? '',
  });

  const [formError, setFormError] = useState<string | null>(null);

  const applyPreset = (preset: Preset) => {
    const filled = preset.apply(slotsPerEpoch);
    setId(filled.id ?? '');
    setDescription(filled.description ?? '');
    setEnabled(true);
    setSlotList(filled.slots_in_epoch.join(','));
    setFromEpoch('');
    setToEpoch('');
    setBidState(initCategoryState(filled.bid, BID_FIELDS, false));
    setApiState(initCategoryState(filled.builder_api, BUILDER_API_FIELDS, false));
    setRevealState(initCategoryState(filled.reveal, REVEAL_FIELDS, false));
    setBuildReorg(filled.build?.reorg_parent_payload ? 'on' : 'off');
    setTransforms({ payload: '', bid: '', envelope: '' });
    setFormError(null);
  };

  // Rules are authored wholesale, so a category resolves to the object itself
  // (custom/disabled) or to "absent" for inherit.
  const resolveRuleCategory = <T,>(
    name: string,
    fields: typeof BID_FIELDS,
    state: CategoryFormState,
    withIgnorePrefs: boolean
  ): { value?: T; error?: string } => {
    const outcome = resolveCategory(name, fields, state, undefined, false, withIgnorePrefs);
    if (outcome.kind === 'error') return { error: outcome.error };
    if (outcome.kind === 'replace') return { value: outcome.obj as T };

    return {};
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);

    // Saving upserts by id, so a new rule reusing one would replace it without
    // a trace — the server only sees the collapsed set and cannot catch it.
    if (!rule && existingIds.includes(id.trim())) {
      setFormError(`A rule with id "${id.trim()}" already exists — edit it or pick another id.`);
      return;
    }

    const { slots, error: slotError } = parseSlotList(slotList, slotsPerEpoch);
    if (slotError) {
      setFormError(slotError);
      return;
    }

    const from = parseOptionalEpoch(fromEpoch, 'From epoch');
    const to = parseOptionalEpoch(toEpoch, 'To epoch');
    if (from.error || to.error) {
      setFormError(from.error || to.error);
      return;
    }

    const bid = resolveRuleCategory<BidPlan>('bid', BID_FIELDS, bidState, true);
    const api = resolveRuleCategory<BuilderAPIPlan>('builder_api', BUILDER_API_FIELDS, apiState, false);
    const reveal = resolveRuleCategory<RevealPlan>('reveal', REVEAL_FIELDS, revealState, false);

    const categoryError = bid.error || api.error || reveal.error;
    if (categoryError) {
      setFormError(categoryError);
      return;
    }

    const next: SlotRule = {
      id: id.trim(),
      enabled,
      slots_in_epoch: slots,
      ...(description.trim() ? { description: description.trim() } : {}),
      ...(from.value !== undefined ? { from_epoch: from.value } : {}),
      ...(to.value !== undefined ? { to_epoch: to.value } : {}),
      ...(bid.value ? { bid: bid.value } : {}),
      ...(api.value ? { builder_api: api.value } : {}),
      ...(reveal.value ? { reveal: reveal.value } : {}),
      ...(buildReorg === 'on' ? { build: { reorg_parent_payload: true } } : {}),
    };

    const transformPlan = {
      ...(transforms.payload.trim() ? { payload: transforms.payload.trim() } : {}),
      ...(transforms.bid.trim() ? { bid: transforms.bid.trim() } : {}),
      ...(transforms.envelope.trim() ? { envelope: transforms.envelope.trim() } : {}),
    };
    if (Object.keys(transformPlan).length > 0) {
      next.transforms = transformPlan;
    }

    onSave(next);
  };

  const disabled = !canEdit || saving;

  return (
    <>
      <div className="modal fade show d-block" tabIndex={-1} role="dialog" aria-modal="true">
        <div className="modal-dialog modal-lg modal-dialog-scrollable">
          <form className="modal-content" onSubmit={handleSubmit}>
            <div className="modal-header py-2">
              <h5 className="modal-title">
                {rule ? `Recurring Rule — ${rule.id}` : 'New Recurring Rule'}
              </h5>
              <button type="button" className="btn-close" aria-label="Close" onClick={onClose}></button>
            </div>

            <div className="modal-body">
              {!rule && (
                <div className="mb-3">
                  <div className="section-header mb-1">Scenario presets</div>
                  {PRESETS.map((preset) => (
                    <button
                      key={preset.id}
                      type="button"
                      className="btn btn-sm btn-outline-primary d-block text-start mb-1 w-100"
                      disabled={disabled}
                      onClick={() => applyPreset(preset)}
                    >
                      <span className="fw-semibold">{preset.label}</span>
                      <span className="d-block form-text mt-0">{preset.hint}</span>
                    </button>
                  ))}
                </div>
              )}

              <div className="row g-2 mb-3">
                <div className="col-12 col-md-6">
                  <label className="form-label small mb-0">Rule id</label>
                  <input
                    type="text"
                    className="form-control form-control-sm"
                    placeholder="slot31-withhold"
                    value={id}
                    disabled={disabled || !!rule}
                    onChange={(e) => setId(e.target.value)}
                  />
                  <div className="form-text mt-0">
                    Lowercase slug; identifies the rule and decides match order when several apply.
                  </div>
                </div>
                <div className="col-12 col-md-6">
                  <label className="form-label small mb-0">Slots in epoch</label>
                  <input
                    type="text"
                    className="form-control form-control-sm"
                    placeholder={slotsPerEpoch > 0 ? String(slotsPerEpoch - 1) : '31'}
                    value={slotList}
                    disabled={disabled}
                    onChange={(e) => setSlotList(e.target.value)}
                  />
                  <div className="form-text mt-0">
                    0-based indices within the epoch: <code>31</code>, <code>0,15,31</code> or{' '}
                    <code>28-31</code>.
                  </div>
                </div>
                <div className="col-12">
                  <label className="form-label small mb-0">Description</label>
                  <input
                    type="text"
                    className="form-control form-control-sm"
                    value={description}
                    disabled={disabled}
                    onChange={(e) => setDescription(e.target.value)}
                  />
                </div>
                <div className="col-6 col-md-3">
                  <label className="form-label small mb-0">From epoch</label>
                  <input
                    type="number"
                    className="form-control form-control-sm"
                    placeholder="always"
                    min={0}
                    value={fromEpoch}
                    disabled={disabled}
                    onChange={(e) => setFromEpoch(e.target.value)}
                  />
                </div>
                <div className="col-6 col-md-3">
                  <label className="form-label small mb-0">To epoch</label>
                  <input
                    type="number"
                    className="form-control form-control-sm"
                    placeholder="forever"
                    min={0}
                    value={toEpoch}
                    disabled={disabled}
                    onChange={(e) => setToEpoch(e.target.value)}
                  />
                </div>
                <div className="col-12 col-md-6 d-flex align-items-end">
                  <div className="form-check">
                    <input
                      className="form-check-input"
                      type="checkbox"
                      id="ap-rule-enabled"
                      checked={enabled}
                      disabled={disabled}
                      onChange={(e) => setEnabled(e.target.checked)}
                    />
                    <label className="form-check-label small" htmlFor="ap-rule-enabled">
                      Enabled
                    </label>
                  </div>
                </div>
              </div>

              <CategoryForm
                title="Bid (p2p)"
                bulk={false}
                state={bidState}
                fields={BID_FIELDS}
                disabled={disabled}
                showIgnorePrefs
                onChange={setBidState}
              />
              <CategoryForm
                title="Builder API"
                bulk={false}
                state={apiState}
                fields={BUILDER_API_FIELDS}
                disabled={disabled}
                onChange={setApiState}
              />
              <CategoryForm
                title="Reveal"
                bulk={false}
                state={revealState}
                fields={REVEAL_FIELDS}
                disabled={disabled}
                onChange={setRevealState}
              />
              <BuildForm bulk={false} value={buildReorg} disabled={disabled} onChange={setBuildReorg} />
              <TransformEditor
                bulk={false}
                value={transforms}
                disabled={disabled}
                sampleSlot={undefined}
                onChange={setTransforms}
              />

              <div className="form-text">
                The rule applies to every matching slot that has no explicit plan of its own. Slots
                that already froze keep the plan they froze with.
              </div>

              {(formError || error) && (
                <div className="alert alert-danger small py-2 mt-2 mb-0">{formError || error}</div>
              )}
            </div>

            <div className="modal-footer py-2">
              <button type="button" className="btn btn-sm btn-secondary" onClick={onClose}>
                Cancel
              </button>
              <button type="submit" className="btn btn-sm btn-primary" disabled={disabled}>
                {saving && <span className="spinner-border spinner-border-sm me-1"></span>}
                {rule ? 'Save Rule' : 'Create Rule'}
              </button>
            </div>
          </form>
        </div>
      </div>
      <div className="modal-backdrop fade show"></div>
    </>
  );
};
