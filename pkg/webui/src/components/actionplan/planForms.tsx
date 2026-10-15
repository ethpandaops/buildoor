import React from 'react';
import type { LocalBuildPlan, ActionMode } from '../../types';
import { InfoHint } from '../InfoHint';

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
  // Explanation shown in the field's info callout.
  hint?: React.ReactNode;
}

// Candidate keys name the parent tuple a payload extends; shared by the bid
// candidate, serve candidates and build candidates explanations.
export const CANDIDATE_KEY_HINTS: Record<string, React.ReactNode> = {
  parent_full: 'the head block on its revealed payload (the normal build)',
  parent_empty: 'the head block, its payload treated as missing (builds on its execution parent; Gloas only)',
  grandparent_full: "the head block's parent on its payload: a deliberate reorg of the head block",
  grandparent_empty: "the head block's parent with its payload treated as missing (reorg + payload miss; Gloas only)",
};

const CandidateKeyList: React.FC = () => (
  <>
    {Object.entries(CANDIDATE_KEY_HINTS).map(([key, text]) => (
      <React.Fragment key={key}>
        <dt>{key}</dt>
        <dd>{text}</dd>
      </React.Fragment>
    ))}
  </>
);

export const BID_FIELDS: FieldDef[] = [
  {
    key: 'bid_start_time', label: 'Bid Start (rel. slot)', unit: 'ms',
    hint: <>When the first bid of the slot may go out, in ms relative to slot start (negative = before the slot starts).</>,
  },
  {
    key: 'bid_end_time', label: 'Bid End (rel. slot)', unit: 'ms',
    hint: <>Last moment a bid is sent, in ms relative to slot start (negative = before the slot starts).</>,
  },
  {
    key: 'bid_min_amount', label: 'Bid Min', unit: 'gwei',
    hint: (
      <>
        Floor of the bid base: <code>max(block value, min) + subsidy</code>. Ignored when a Bid Value
        Override applies.
      </>
    ),
  },
  {
    key: 'bid_increase', label: 'Bid Increase', unit: 'gwei',
    hint: (
      <>
        Added once per earlier bid of the same payload, so every re-bid lands one step higher. Also
        applies on top of a Bid Value Override.
      </>
    ),
  },
  {
    key: 'bid_interval', label: 'Bid Interval', unit: 'ms',
    hint: <>Pause between successive bids of the same payload inside the bid window. <code>0</code> = a single bid.</>,
  },
  {
    key: 'bid_subsidy', label: 'Bid Subsidy', unit: 'gwei',
    hint: (
      <>
        Added <strong>on top of</strong> <code>max(block value, min)</code>, so it outbids competitors
        whatever the block is worth. Ignored when a Bid Value Override applies.
      </>
    ),
  },
  {
    key: 'bid_value_gwei', label: 'Bid Value Override', unit: 'gwei',
    hint: (
      <>
        Absolute bid base that <strong>replaces</strong> <code>max(block value, min) + subsidy</code>;
        Bid Increase still applies per re-bid. Can deliberately underbid the block value, and silently
        loses the auction when set below the other builders' bids.
      </>
    ),
  },
  {
    key: 'bid_candidate', label: 'Bid Candidate', unit: '',
    options: ['auto', 'all', 'parent_full', 'parent_empty', 'grandparent_full', 'grandparent_empty'],
    hint: (
      <>
        Which built candidate payload the p2p bids commit to. A specific key only bids when that
        candidate was built for the slot.
        <dl>
          <dt>inherit</dt>
          <dd>the global ePBS bid candidate setting</dd>
          <dt>auto</dt>
          <dd>the candidate matching the chain view (head block + its payload status); sticky for the slot unless candidate switching is enabled globally</dd>
          <dt>all</dt>
          <dd>a bid for every built candidate (multi-parent gossip testing; most nodes only propagate a builder's first bid per slot)</dd>
          <CandidateKeyList />
        </dl>
      </>
    ),
  },
];

export const BUILDER_API_FIELDS: FieldDef[] = [
  {
    key: 'value_subsidy_gwei', label: 'Value Subsidy', unit: 'gwei',
    hint: (
      <>
        Added to the block value of bids served to proposers; replaces the global Builder API subsidy
        for this slot. Ignored when a Total Value Override applies.
      </>
    ),
  },
  {
    key: 'total_value_override_gwei', label: 'Total Value Override', unit: 'gwei',
    hint: (
      <>
        Absolute total bid value served to the proposer, <strong>replacing</strong> block value +
        subsidy (before the Gloas execution-payment split). May exceed the block value.
      </>
    ),
  },
  {
    key: 'execution_payment_gwei', label: 'Execution Payment', unit: 'gwei',
    hint: (
      <>
        Gloas bids only: the part of the served total claimed as <code>execution_payment</code> instead
        of the trustless on-chain value. Deliberately unbacked, buildoor never makes the EL payment.
        Capped by the proposer's advertised <code>max_execution_payment</code> (0 when none was
        submitted) unless Ignore Pref Limit is true. A non-zero value wins over the percent;{' '}
        <code>0</code> = none.
      </>
    ),
  },
  {
    key: 'execution_payment_percent', label: 'Execution Payment', unit: '%',
    hint: (
      <>
        The execution payment as a percentage (0–100) of the served total. Only used while the
        absolute Execution Payment (gwei) is 0; same cap and unbacked claim.
      </>
    ),
  },
  {
    key: 'ignore_preference_limit', label: 'Ignore Pref Limit', unit: '',
    options: ['true', 'false'], bool: true,
    hint: (
      <>
        Whether the execution payment may exceed the proposer's advertised{' '}
        <code>max_execution_payment</code>.
        <dl>
          <dt>inherit</dt>
          <dd>the global setting</dd>
          <dt>true</dt>
          <dd>serve beyond the limit: a spec-violating bid clients should reject</dd>
          <dt>false</dt>
          <dd>cap the execution payment at the proposer's limit</dd>
        </dl>
      </>
    ),
  },
  {
    key: 'response_delay_ms', label: 'Response Delay', unit: 'ms',
    hint: (
      <>
        Holds the bid response this long before answering (0 up to one slot). Ends early, without
        serving, when the requesting client gives up.
      </>
    ),
  },
  {
    key: 'serve_candidates', label: 'Serve Candidates', unit: '',
    options: ['all', 'canonical_only', 'parent_full', 'parent_empty', 'grandparent_full', 'grandparent_empty'],
    hint: (
      <>
        Which built candidate payloads a bid request for this slot may be answered from.
        <dl>
          <dt>inherit</dt>
          <dd>the global Builder API setting</dd>
          <dt>all</dt>
          <dd>whichever candidate matches the parent the proposer asks for</dd>
          <dt>canonical_only</dt>
          <dd>only parent_full (and unclassified) payloads</dd>
          <dt>a candidate key</dt>
          <dd>only that candidate:</dd>
          <CandidateKeyList />
        </dl>
      </>
    ),
  },
];

export const REVEAL_FIELDS: FieldDef[] = [
  {
    key: 'reveal_time_ms', label: 'Reveal Time (rel. slot)', unit: 'ms',
    hint: (
      <>
        Time gate: when the envelope is published, in ms relative to slot start. A custom reveal may
        run past the slot end (at most one extra slot) for late-reveal testing.
      </>
    ),
  },
  {
    key: 'gate_mode', label: 'Gate Mode', unit: '',
    options: ['time', 'vote', 'vote_or_time', 'vote_and_time'],
    hint: (
      <>
        What opens the reveal. A vote gate that never opens withholds the payload at the deadline
        (<code>vote_gate_timeout</code>).
        <dl>
          <dt>inherit</dt>
          <dd>the global reveal gate mode</dd>
          <dt>time</dt>
          <dd>at the Reveal Time</dd>
          <dt>vote</dt>
          <dd>as soon as head-vote participation on our block reaches the Vote Threshold</dd>
          <dt>vote_or_time</dt>
          <dd>whichever of the two opens first</dd>
          <dt>vote_and_time</dt>
          <dd>at the Reveal Time, but only once the threshold is also reached</dd>
        </dl>
      </>
    ),
  },
  {
    key: 'vote_threshold_pct', label: 'Vote Threshold', unit: '%',
    hint: (
      <>
        Head-vote participation (percent of the slot's attesting balance voting for the block that
        committed to our bid) that opens the vote gate. Unused with gate mode <code>time</code>.
      </>
    ),
  },
  {
    key: 'broadcast_validation', label: 'Broadcast Validation', unit: '',
    options: ['gossip', 'consensus', 'consensus_and_equivocation'],
    hint: (
      <>
        Validation the beacon node applies before broadcasting the envelope.
        <dl>
          <dt>inherit</dt>
          <dd>the global reveal setting</dd>
          <dt>gossip</dt>
          <dd>gossip checks only (fastest)</dd>
          <dt>consensus</dt>
          <dd>full consensus validation first</dd>
          <dt>consensus_and_equivocation</dt>
          <dd>consensus validation plus an equivocation check: the builder's protection against unbundling</dd>
        </dl>
      </>
    ),
  },
];

// Category explanations for the section headers of the plan and rule editors.
export const BID_CATEGORY_HINT: React.ReactNode = (
  <>
    p2p bidding (ePBS, Gloas+): bids gossiped to the network inside the bid window. Bidding still
    needs the Gloas fork and an active builder registration.
  </>
);

export const BUILDER_API_CATEGORY_HINT: React.ReactNode = (
  <>
    Bids served to proposers asking through the Builder API (<code>getHeader</code> /{' '}
    <code>getExecutionPayloadBid</code>). A suppressed slot answers 204 (no bid). Serving still needs
    the API server to be running.
  </>
);

export const REVEAL_CATEGORY_HINT: React.ReactNode = (
  <>
    Publishing the execution payload envelope of a won Gloas slot, for p2p and Builder API wins
    alike. <strong>disabled</strong> withholds it: the block exists, its payload does not (recorded
    as <code>suppressed</code> / <code>plan_disabled</code>).
  </>
);

// The mode select of a category: what each choice does for the slot.
const ModeHint: React.FC<{ bulk: boolean }> = ({ bulk }) => (
  <InfoHint>
    How this category runs for the slot. Availability still wins: a custom slot cannot act
    without the fork, registration or server it needs.
    <dl>
      {bulk && (
        <>
          <dt>unchanged</dt>
          <dd>leave this category as it is on every targeted slot</dd>
        </>
      )}
      <dt>{bulk ? 'inherit (clear)' : 'inherit'}</dt>
      <dd>no instruction: follow the global config, including the module's enable flag</dd>
      <dt>custom</dt>
      <dd>force the category active for the slot, even when the module is globally disabled; filled fields override the global values</dd>
      <dt>disabled</dt>
      <dd>suppress the category for the slot</dd>
    </dl>
  </InfoHint>
);

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
  hint?: React.ReactNode;
  bulk: boolean;
  state: CategoryFormState;
  fields: FieldDef[];
  disabled: boolean;
  showIgnorePrefs?: boolean;
  onChange: (next: CategoryFormState) => void;
}> = ({ title, hint, bulk, state, fields, disabled, showIgnorePrefs, onChange }) => (
  <div className="mb-3">
    <div className="d-flex align-items-center gap-2 mb-1">
      <div className="section-header">
        {title}
        {hint && <InfoHint>{hint}</InfoHint>}
      </div>
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
      <ModeHint bulk={bulk} />
    </div>

    {state.mode === 'custom' && (
      <div className="row g-2">
        {fields.map((def, i) => (
          <div key={def.key} className="col-6 col-lg-4">
            <label className="form-label small mb-0">
              {def.label}
              {/* The third column opens its callout leftwards to stay inside the modal. */}
              {def.hint && <InfoHint alignEnd={i % 3 === 2}>{def.hint}</InfoHint>}
            </label>
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
              <InfoHint>
                Without gossip proposer preferences for the slot the bidder normally skips it. Checked,
                it bids anyway, using the built payload's fee recipient.
              </InfoHint>
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
      <div className="section-header">
        Build
        <InfoHint>
          Changes how the slot's payload is built when a build happens; it never forces or
          suppresses the build itself.
          <dl>
            {bulk && (
              <>
                <dt>unchanged</dt>
                <dd>leave the flag as it is on every targeted slot</dd>
              </>
            )}
            <dt>normal parent</dt>
            <dd>build on the immediate parent payload</dd>
            <dt>reorg parent (build on n-2)</dt>
            <dd>
              build on the grandparent (n-2) execution payload: parent hash, parent number and
              withdrawals come from the parent slot's attributes, everything else from this slot.
              Rejected by mainnet forkchoice; falls back to a normal build when the parent slot's
              attributes are unavailable.
            </dd>
          </dl>
        </InfoHint>
      </div>
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
  // Length of an explicit list the backend hid from this reader (no login);
  // 0 once the list is visible or replaced.
  transactions_redacted: number;
  queued: string; // one pool tx hash per line, exact order
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
  transactions_redacted: 0,
  queued: '',
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
    tx_source: plan.tx_source ??
      (plan.transactions?.length || plan.transactions_redacted ? 'explicit' : plan.queued?.length ? 'queued' : ''),
    transactions: (plan.transactions ?? []).join('\n'),
    transactions_redacted: plan.transactions?.length ? 0 : plan.transactions_redacted ?? 0,
    queued: (plan.queued ?? []).join('\n'),
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
  const queued = state.queued
    .split(/\s+/)
    .map((t) => t.trim())
    .filter((t) => t !== '');
  if (state.tx_source === 'explicit' && txs.length === 0 && state.transactions_redacted > 0) {
    // The hidden list stays a marker: saving it is blocked (hiddenTransactionsError)
    // and refused by the backend.
    plan.tx_source = 'explicit';
    plan.transactions_redacted = state.transactions_redacted;
  } else if (state.tx_source === 'explicit' || (state.tx_source === '' && txs.length > 0)) {
    plan.tx_source = 'explicit';
    plan.transactions = txs;
  } else if (state.tx_source === 'queued' || (state.tx_source === '' && queued.length > 0)) {
    plan.tx_source = 'queued';
    plan.queued = queued;
  } else if (state.tx_source) {
    plan.tx_source = state.tx_source;
  }
  if (state.build_el_payload !== '') plan.build_el_payload = state.build_el_payload === 'true';
  if (state.max_txs.trim() !== '') plan.max_txs = Number(state.max_txs);
  if (state.gas_fill_pct.trim() !== '') plan.gas_fill_pct = Number(state.gas_fill_pct);
  if (state.ordering) plan.ordering = state.ordering;
  return Object.keys(plan).length > 0 ? plan : undefined;
}

// hiddenTransactionsError explains why a plan carrying a hidden explicit list
// cannot be saved: the raw transactions were redacted for a reader without a
// login, so writing the plan back would lose them.
export function hiddenTransactionsError(plan: LocalBuildPlan | undefined): string | null {
  if (!plan?.transactions_redacted || plan.transactions?.length) return null;
  return `The custom build's ${plan.transactions_redacted} explicit transactions are hidden (no login): ` +
    'log in and reopen the plan to edit them, or replace the list before saving.';
}

// HiddenTransactionsNote marks an explicit transaction list the backend hid
// from a reader without a login.
export const HiddenTransactionsNote: React.FC<{ count: number }> = ({ count }) => (
  <span className="text-muted small">
    <i className="fas fa-lock me-1"></i>
    {count} explicit transaction{count === 1 ? '' : 's'} hidden (log in to view/edit)
  </span>
);

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
  const isQueued = state.tx_source === 'queued';

  return (
    <div className="mb-3">
      <div className="d-flex align-items-center gap-2 mb-1">
        <div className="section-header">
          Custom build
          <InfoHint>
            The testing_buildBlockV1 build next to the engine build: buildoor hands the EL the
            transaction list. Overrides the global Custom Build settings for these slots.
          </InfoHint>
        </div>
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
            <label className="form-label small mb-0">
              Bid with
              <InfoHint>
                Which block feeds bids and the reveal. <strong>engine</strong>: the regular build, the
                custom block is compare-only. <strong>custom</strong>: the custom block, no fallback.{' '}
                <strong>custom, else engine</strong>: the custom block when built, otherwise the engine block.{' '}
                <strong>higher-value</strong>: both are built, the one worth more wins (engine on a tie).
              </InfoHint>
            </label>
            <select
              className="form-select form-select-sm"
              value={state.payload_source}
              disabled={disabled}
              onChange={(e) => set({ payload_source: e.target.value })}
            >
              <option value="">inherit</option>
              <option value="el">engine block (custom is compare-only)</option>
              <option value="local">custom block (no fallback)</option>
              <option value="local_or_el">custom block, else engine block</option>
              <option value="higher_value">higher-value block</option>
            </select>
          </div>
          <div className="col-6">
            <label className="form-label small mb-0">
              Custom block transactions
              <InfoHint>
                <strong>buildoor mempool</strong>: transactions sent to <code>/rpc</code>.{' '}
                <strong>buildoor mempool, then EL mempool</strong>: ours first, the EL&apos;s pending
                transactions fill the remaining gas (highest tip first, no blob transactions).{' '}
                <strong>EL mempool</strong>: the EL fills the block itself.{' '}
                <strong>explicit</strong>: exactly the raw transactions below.{' '}
                <strong>queued</strong>: exactly these buildoor-mempool hashes, in this order. Any
                deviation fails the build instead of trimming the list.
              </InfoHint>
            </label>
            <select
              className="form-select form-select-sm"
              value={state.tx_source}
              disabled={disabled}
              onChange={(e) => set({ tx_source: e.target.value })}
            >
              <option value="">inherit</option>
              <option value="txpool">buildoor mempool</option>
              <option value="txpool_el_fill">buildoor mempool, then EL mempool</option>
              <option value="el_mempool">EL mempool</option>
              <option value="empty">none (empty block)</option>
              <option value="explicit">explicit list (raw txs)</option>
              <option value="queued">queued list (pool tx hashes, exact order)</option>
            </select>
          </div>
          {isQueued && (
            <div className="col-12">
              <label className="form-label small mb-0">Queued tx hashes (one per line, exact block order)</label>
              <textarea
                className="form-control form-control-sm font-monospace"
                rows={4}
                value={state.queued}
                disabled={disabled}
                placeholder="0x…"
                onChange={(e) => set({ queued: e.target.value })}
              />
              <div className="form-text mt-0">
                Every hash must be queued in the pool at build time and respect the senders' nonce
                order; any deviation fails the build instead of trimming the list.
              </div>
            </div>
          )}
          {isExplicit && state.transactions_redacted > 0 && state.transactions.trim() === '' && (
            <div className="col-12">
              <div className="alert alert-secondary small py-1 px-2 mb-0 d-flex flex-wrap align-items-center gap-2">
                <HiddenTransactionsNote count={state.transactions_redacted} />
                <button
                  type="button"
                  className="btn btn-sm btn-outline-secondary py-0 ms-auto"
                  disabled={disabled}
                  onClick={() => set({ transactions_redacted: 0 })}
                >
                  Replace list
                </button>
              </div>
            </div>
          )}
          {isExplicit && !(state.transactions_redacted > 0 && state.transactions.trim() === '') && (
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
            <label className="form-label small mb-0">
              Keep engine build
              <InfoHint>
                When bidding with the custom block only: keep building the engine block for
                comparison.
              </InfoHint>
            </label>
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
              <option value="round_robin">round robin (one per sender per round)</option>
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
