import React from 'react';
import type { ActionMode } from '../../types';

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
}

export const BID_FIELDS: FieldDef[] = [
  { key: 'bid_start_time', label: 'Bid Start (rel. slot)', unit: 'ms' },
  { key: 'bid_end_time', label: 'Bid End (rel. slot)', unit: 'ms' },
  { key: 'bid_min_amount', label: 'Bid Min', unit: 'gwei' },
  { key: 'bid_increase', label: 'Bid Increase', unit: 'gwei' },
  { key: 'bid_interval', label: 'Bid Interval', unit: 'ms' },
  { key: 'bid_subsidy', label: 'Bid Subsidy', unit: 'gwei' },
  { key: 'bid_value_gwei', label: 'Bid Value Override', unit: 'gwei' },
];

export const BUILDER_API_FIELDS: FieldDef[] = [
  { key: 'value_subsidy_gwei', label: 'Value Subsidy', unit: 'gwei' },
  { key: 'total_value_override_gwei', label: 'Total Value Override', unit: 'gwei' },
  { key: 'response_delay_ms', label: 'Response Delay', unit: 'ms' },
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
    if (typeof value === 'number' || typeof value === 'string') {
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
): { values: Record<string, number | string>; error: string | null } {
  const values: Record<string, number | string> = {};

  for (const def of defs) {
    const raw = (state.fields[def.key] ?? '').trim();
    if (raw === '') continue;

    if (def.options) {
      if (!def.options.includes(raw)) {
        return { values, error: `${name}: ${def.label} must be one of ${def.options.join(', ')}` };
      }
      values[def.key] = raw;
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
          const oldValue = typeof rawOld === 'number' || typeof rawOld === 'string'
            ? (rawOld as number | string)
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
