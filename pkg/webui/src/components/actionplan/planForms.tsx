import React from 'react';
import type { LocalBuildPlan, ActionMode } from '../../types';

// Shared plan-category form model, used by both the per-slot edit modal and
// the recurring rule editor: the two author the same categories, only their
// target differs (a slot's plan vs. a rule's template).
// ---------------------------------------------------------------------------
// Edit form model
// ---------------------------------------------------------------------------

// 'unchanged' exists only in bulk mode (leave targeted slots as they are);
// 'inherit' clears the category back to the global baseline.
export type FormMode = 'unchanged' | 'inherit' | 'custom' | 'disabled';

export interface FieldDef {
  key: string;
  label: string;
  unit: 'ms' | 'gwei' | '%' | '';
  // Enum fields render a select instead of a number input; the raw string
  // value is sent as the override.
  options?: string[];
  // Boolean fields render as a true/false select (options must be
  // ['true', 'false']) and are sent as real booleans.
  bool?: boolean;
}

export const BID_FIELDS: FieldDef[] = [
  { key: 'bid_start_time', label: 'Bid Start (rel. slot)', unit: 'ms' },
  { key: 'bid_end_time', label: 'Bid End (rel. slot)', unit: 'ms' },
  { key: 'bid_min_amount', label: 'Bid Min', unit: 'gwei' },
  { key: 'bid_increase', label: 'Bid Increase', unit: 'gwei' },
  { key: 'bid_interval', label: 'Bid Interval', unit: 'ms' },
  { key: 'bid_subsidy', label: 'Bid Subsidy', unit: 'gwei' },
  { key: 'bid_value_gwei', label: 'Bid Value Override', unit: 'gwei' },
  {
    key: 'bid_candidate', label: 'Bid Candidate', unit: '',
    options: ['auto', 'all', 'parent_full', 'parent_empty', 'grandparent_full', 'grandparent_empty'],
  },
];

export const BUILDER_API_FIELDS: FieldDef[] = [
  { key: 'value_subsidy_gwei', label: 'Value Subsidy', unit: 'gwei' },
  { key: 'total_value_override_gwei', label: 'Total Value Override', unit: 'gwei' },
  { key: 'execution_payment_gwei', label: 'Execution Payment', unit: 'gwei' },
  { key: 'execution_payment_percent', label: 'Execution Payment', unit: '%' },
  {
    key: 'ignore_preference_limit', label: 'Ignore Pref Limit', unit: '',
    options: ['true', 'false'], bool: true,
  },
  { key: 'response_delay_ms', label: 'Response Delay', unit: 'ms' },
  {
    key: 'serve_candidates', label: 'Serve Candidates', unit: '',
    options: ['all', 'canonical_only', 'parent_full', 'parent_empty', 'grandparent_full', 'grandparent_empty'],
  },
];

export const REVEAL_FIELDS: FieldDef[] = [
  { key: 'reveal_time_ms', label: 'Reveal Time (rel. slot)', unit: 'ms' },
  {
    key: 'gate_mode', label: 'Gate Mode', unit: '',
    options: ['time', 'vote', 'vote_or_time', 'vote_and_time'],
  },
  { key: 'vote_threshold_pct', label: 'Vote Threshold', unit: '%' },
  {
    key: 'broadcast_validation', label: 'Broadcast Validation', unit: '',
    options: ['gossip', 'consensus', 'consensus_and_equivocation'],
  },
];

export interface CategoryFormState {
  mode: FormMode;
  fields: Record<string, string>; // raw input values; empty = inherit
  ignoreMissingPrefs: boolean;
}

export function initCategoryState(
  category: { mode: ActionMode } | undefined,
  defs: FieldDef[],
  bulk: boolean
): CategoryFormState {
  if (bulk || !category) {
    return { mode: bulk ? 'unchanged' : 'inherit', fields: {}, ignoreMissingPrefs: false };
  }

  const raw = category as unknown as Record<string, unknown>;
  const fields: Record<string, string> = {};
  for (const def of defs) {
    const value = raw[def.key];
    if (typeof value === 'number' || typeof value === 'string' || typeof value === 'boolean') {
      fields[def.key] = String(value);
    }
  }

  return {
    mode: category.mode,
    fields,
    ignoreMissingPrefs: raw['ignore_missing_prefs'] === true,
  };
}

export function parseCategoryFields(
  name: string,
  defs: FieldDef[],
  state: CategoryFormState
): { values: Record<string, number | string | boolean>; error: string | null } {
  const values: Record<string, number | string | boolean> = {};

  for (const def of defs) {
    const raw = (state.fields[def.key] ?? '').trim();
    if (raw === '') continue;

    if (def.options) {
      if (!def.options.includes(raw)) {
        return { values, error: `${name}: ${def.label} must be one of ${def.options.join(', ')}` };
      }
      values[def.key] = def.bool ? raw === 'true' : raw;
      continue;
    }

    const num = Number(raw);
    if (!Number.isFinite(num) || !Number.isInteger(num)) {
      return { values, error: `${name}: ${def.label} must be an integer (${def.unit})` };
    }
    values[def.key] = num;
  }

  return { values, error: null };
}

export type CategoryOutcome =
  | { kind: 'none' }
  | { kind: 'clear' }
  | { kind: 'replace'; obj: Record<string, unknown> }
  | { kind: 'set'; paths: Record<string, number | string | boolean | null> }
  | { kind: 'error'; error: string };

// resolveCategory turns one category form into its PlanUpdate contribution.
// Single-slot edits of an existing custom category use fine-grained `set`
// paths so unchanged sibling fields are never clobbered; mode switches send
// the full category object.
export function resolveCategory(
  name: string,
  defs: FieldDef[],
  state: CategoryFormState,
  initial: Record<string, unknown> | undefined,
  single: boolean,
  withIgnorePrefs: boolean
): CategoryOutcome {
  switch (state.mode) {
    case 'unchanged':
      return { kind: 'none' };

    case 'inherit':
      if (single && !initial) return { kind: 'none' };
      return { kind: 'clear' };

    case 'disabled':
      if (single && initial && initial['mode'] === 'disabled') return { kind: 'none' };
      return { kind: 'replace', obj: { mode: 'disabled' } };

    case 'custom': {
      const { values, error } = parseCategoryFields(name, defs, state);
      if (error) return { kind: 'error', error };

      if (single && initial && initial['mode'] === 'custom') {
        const paths: Record<string, number | string | boolean | null> = {};

        for (const def of defs) {
          const rawOld = initial[def.key];
          const oldValue =
            typeof rawOld === 'number' || typeof rawOld === 'string' || typeof rawOld === 'boolean'
              ? (rawOld as number | string | boolean)
              : undefined;
          const newValue = values[def.key];

          if (newValue === undefined && oldValue !== undefined) {
            paths[`${name}.${def.key}`] = null; // clear one override
          } else if (newValue !== undefined && newValue !== oldValue) {
            paths[`${name}.${def.key}`] = newValue;
          }
        }

        if (withIgnorePrefs) {
          const oldFlag = initial['ignore_missing_prefs'] === true;
          if (state.ignoreMissingPrefs !== oldFlag) {
            paths[`${name}.ignore_missing_prefs`] = state.ignoreMissingPrefs;
          }
        }

        if (Object.keys(paths).length === 0) return { kind: 'none' };
        return { kind: 'set', paths };
      }

      const obj: Record<string, unknown> = { mode: 'custom', ...values };
      if (withIgnorePrefs && state.ignoreMissingPrefs) {
        obj['ignore_missing_prefs'] = true;
      }
      return { kind: 'replace', obj };
    }
  }
}

// ---------------------------------------------------------------------------
// Category form section
// ---------------------------------------------------------------------------

export const CategoryForm: React.FC<{
  title: string;
  bulk: boolean;
  state: CategoryFormState;
  fields: FieldDef[];
  disabled: boolean;
  showIgnorePrefs?: boolean;
  onChange: (next: CategoryFormState) => void;
}> = ({ title, bulk, state, fields, disabled, showIgnorePrefs, onChange }) => (
  <div className="mb-3">
    <div className="d-flex align-items-center gap-2 mb-1">
      <div className="section-header">{title}</div>
      <select
        className="form-select form-select-sm w-auto"
        value={state.mode}
        disabled={disabled}
        onChange={(e) => onChange({ ...state, mode: e.target.value as FormMode })}
      >
        {bulk && <option value="unchanged">unchanged</option>}
        <option value="inherit">{bulk ? 'inherit (clear)' : 'inherit'}</option>
        <option value="custom">custom</option>
        <option value="disabled">disabled</option>
      </select>
    </div>

    {state.mode === 'custom' && (
      <div className="row g-2">
        {fields.map((def) => (
          <div key={def.key} className="col-6 col-lg-4">
            <label className="form-label small mb-0">{def.label}</label>
            {def.options ? (
              <select
                className="form-select form-select-sm"
                value={state.fields[def.key] ?? ''}
                disabled={disabled}
                onChange={(e) =>
                  onChange({ ...state, fields: { ...state.fields, [def.key]: e.target.value } })
                }
              >
                <option value="">inherit</option>
                {def.options.map((opt) => (
                  <option key={opt} value={opt}>{opt}</option>
                ))}
              </select>
            ) : (
              <div className="input-group input-group-sm">
                <input
                  type="number"
                  className="form-control"
                  placeholder="inherit"
                  value={state.fields[def.key] ?? ''}
                  disabled={disabled}
                  onChange={(e) =>
                    onChange({ ...state, fields: { ...state.fields, [def.key]: e.target.value } })
                  }
                />
                <span className="input-group-text">{def.unit}</span>
              </div>
            )}
          </div>
        ))}
        {showIgnorePrefs && (
          <div className="col-12">
            <div className="form-check">
              <input
                className="form-check-input"
                type="checkbox"
                id="ap-ignore-prefs"
                checked={state.ignoreMissingPrefs}
                disabled={disabled}
                onChange={(e) => onChange({ ...state, ignoreMissingPrefs: e.target.checked })}
              />
              <label className="form-check-label small" htmlFor="ap-ignore-prefs">
                Ignore missing proposer preferences (bid with the payload's fee recipient)
              </label>
            </div>
          </div>
        )}
        <div className="col-12 form-text mt-0">
          Empty fields inherit the global config. Timing values are signed ms relative to slot start.
        </div>
      </div>
    )}
  </div>
);

// ---------------------------------------------------------------------------
// Build category (modeless single flag)
// ---------------------------------------------------------------------------

// 'unchanged' only exists in bulk mode; 'off' = normal parent, 'on' = reorg.
export type BuildFlagMode = 'unchanged' | 'off' | 'on';

export const BuildForm: React.FC<{
  bulk: boolean;
  value: BuildFlagMode;
  disabled: boolean;
  onChange: (next: BuildFlagMode) => void;
}> = ({ bulk, value, disabled, onChange }) => (
  <div className="mb-3">
    <div className="d-flex align-items-center gap-2 mb-1">
      <div className="section-header">Build</div>
      <select
        className="form-select form-select-sm w-auto"
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value as BuildFlagMode)}
      >
        {bulk && <option value="unchanged">unchanged</option>}
        <option value="off">normal parent</option>
        <option value="on">reorg parent (build on n-2)</option>
      </select>
    </div>
    {value === 'on' && (
      <div className="form-text mt-0">
        Builds the payload on the grandparent (n-2) execution payload instead of the immediate
        parent — parent hash, parent number and withdrawals come from the parent slot, everything
        else from this slot. A deliberate parent-payload reorg attempt; rejected by mainnet
        forkchoice, useful for testing.
      </div>
    )}
  </div>
);

// ---------------------------------------------------------------------------
// Local build (modeless overrides of the testing_buildBlockV1 extension)
// ---------------------------------------------------------------------------

// Every field is tri-state-ish: '' inherits the global setting.
export interface LocalBuildFormState {
  enabled: '' | 'true' | 'false';
  payload_source: string;
  tx_source: string;
  transactions: string; // one 0x-hex raw transaction per line
  build_el_payload: '' | 'true' | 'false';
  max_txs: string;
  gas_fill_pct: string;
  ordering: string;
}

export const EMPTY_LOCAL_BUILD_STATE: LocalBuildFormState = {
  enabled: '',
  payload_source: '',
  tx_source: '',
  transactions: '',
  build_el_payload: '',
  max_txs: '',
  gas_fill_pct: '',
  ordering: '',
};

export function initLocalBuildState(plan: LocalBuildPlan | undefined): LocalBuildFormState {
  if (!plan) return EMPTY_LOCAL_BUILD_STATE;
  return {
    enabled: plan.enabled === undefined ? '' : plan.enabled ? 'true' : 'false',
    payload_source: plan.payload_source ?? '',
    tx_source: plan.tx_source ?? (plan.transactions?.length ? 'explicit' : ''),
    transactions: (plan.transactions ?? []).join('\n'),
    build_el_payload: plan.build_el_payload === undefined ? '' : plan.build_el_payload ? 'true' : 'false',
    max_txs: plan.max_txs === undefined ? '' : String(plan.max_txs),
    gas_fill_pct: plan.gas_fill_pct === undefined ? '' : String(plan.gas_fill_pct),
    ordering: plan.ordering ?? '',
  };
}

// localBuildPlanFromState returns the plan object, or undefined when every
// field inherits (the category member is then omitted / cleared).
export function localBuildPlanFromState(state: LocalBuildFormState): LocalBuildPlan | undefined {
  const plan: LocalBuildPlan = {};
  if (state.enabled !== '') plan.enabled = state.enabled === 'true';
  if (state.payload_source) plan.payload_source = state.payload_source;
  const txs = state.transactions
    .split(/\s+/)
    .map((t) => t.trim())
    .filter((t) => t !== '');
  if (state.tx_source === 'explicit' || (state.tx_source === '' && txs.length > 0)) {
    plan.tx_source = 'explicit';
    plan.transactions = txs;
  } else if (state.tx_source) {
    plan.tx_source = state.tx_source;
  }
  if (state.build_el_payload !== '') plan.build_el_payload = state.build_el_payload === 'true';
  if (state.max_txs.trim() !== '') plan.max_txs = Number(state.max_txs);
  if (state.gas_fill_pct.trim() !== '') plan.gas_fill_pct = Number(state.gas_fill_pct);
  if (state.ordering) plan.ordering = state.ordering;
  return Object.keys(plan).length > 0 ? plan : undefined;
}

export const LocalBuildForm: React.FC<{
  bulk: boolean;
  state: LocalBuildFormState;
  disabled: boolean;
  available: boolean;
  unavailableReason?: string;
  onChange: (next: LocalBuildFormState) => void;
}> = ({ bulk, state, disabled, available, unavailableReason, onChange }) => {
  const set = (patch: Partial<LocalBuildFormState>) => onChange({ ...state, ...patch });
  const isExplicit = state.tx_source === 'explicit';

  return (
    <div className="mb-3">
      <div className="d-flex align-items-center gap-2 mb-1">
        <div className="section-header">Local build</div>
        <select
          className="form-select form-select-sm w-auto"
          value={state.enabled}
          disabled={disabled}
          onChange={(e) => set({ enabled: e.target.value as LocalBuildFormState['enabled'] })}
        >
          <option value="">{bulk ? 'unchanged / inherit' : 'inherit'}</option>
          <option value="true">enabled</option>
          <option value="false">disabled</option>
        </select>
        {!available && (
          <span className="badge bg-warning text-dark" title={unavailableReason}>
            EL testing namespace unavailable
          </span>
        )}
      </div>
      {!available && state.enabled === 'true' && (
        <div className="form-text mt-0 text-warning">
          {unavailableReason || 'testing_buildBlockV1 is not available on the EL'} — the plan is rejected
          until the namespace is enabled; availability wins at build time.
        </div>
      )}
      {(state.enabled === 'true' || state.enabled === '') && (
        <div className="row g-2">
          <div className="col-6">
            <label className="form-label small mb-0">Payload source</label>
            <select
              className="form-select form-select-sm"
              value={state.payload_source}
              disabled={disabled}
              onChange={(e) => set({ payload_source: e.target.value })}
            >
              <option value="">inherit</option>
              <option value="el">el (local is a shadow)</option>
              <option value="local">local (strict)</option>
              <option value="local_or_el">local, else el</option>
            </select>
          </div>
          <div className="col-6">
            <label className="form-label small mb-0">Tx source</label>
            <select
              className="form-select form-select-sm"
              value={state.tx_source}
              disabled={disabled}
              onChange={(e) => set({ tx_source: e.target.value })}
            >
              <option value="">inherit</option>
              <option value="txpool">txpool</option>
              <option value="empty">empty block</option>
              <option value="el_mempool">EL mempool (testing path)</option>
              <option value="explicit">explicit list</option>
            </select>
          </div>
          {isExplicit && (
            <div className="col-12">
              <label className="form-label small mb-0">Transactions (0x-hex raw, one per line)</label>
              <textarea
                className="form-control form-control-sm font-monospace"
                rows={4}
                value={state.transactions}
                disabled={disabled}
                placeholder="0x02f8..."
                onChange={(e) => set({ transactions: e.target.value })}
              />
            </div>
          )}
          <div className="col-4">
            <label className="form-label small mb-0">Keep EL build</label>
            <select
              className="form-select form-select-sm"
              value={state.build_el_payload}
              disabled={disabled}
              onChange={(e) => set({ build_el_payload: e.target.value as LocalBuildFormState['build_el_payload'] })}
            >
              <option value="">inherit</option>
              <option value="true">yes</option>
              <option value="false">no</option>
            </select>
          </div>
          <div className="col-4">
            <label className="form-label small mb-0">Max txs</label>
            <input
              type="number"
              min={0}
              className="form-control form-control-sm"
              value={state.max_txs}
              disabled={disabled}
              placeholder="inherit"
              onChange={(e) => set({ max_txs: e.target.value })}
            />
          </div>
          <div className="col-4">
            <label className="form-label small mb-0">Gas fill %</label>
            <input
              type="number"
              min={1}
              max={100}
              className="form-control form-control-sm"
              value={state.gas_fill_pct}
              disabled={disabled}
              placeholder="inherit"
              onChange={(e) => set({ gas_fill_pct: e.target.value })}
            />
          </div>
          <div className="col-6">
            <label className="form-label small mb-0">Ordering</label>
            <select
              className="form-select form-select-sm"
              value={state.ordering}
              disabled={disabled}
              onChange={(e) => set({ ordering: e.target.value })}
            >
              <option value="">inherit</option>
              <option value="fifo">fifo (arrival)</option>
              <option value="tip">tip (highest first)</option>
              <option value="random">random</option>
            </select>
          </div>
          <div className="col-12 form-text mt-0">
            Builds an additional payload through the EL's testing_buildBlockV1 from the chosen
            transaction source; the payload source decides whether bids use it or the EL payload.
          </div>
        </div>
      )}
    </div>
  );
};
