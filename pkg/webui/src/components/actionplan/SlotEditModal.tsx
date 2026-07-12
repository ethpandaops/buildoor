import React, { useState } from 'react';
import type {
  ActionMode,
  FrozenPlan,
  PlanUpdate,
  SlotPlan,
  SlotResult,
} from '../../types';
import type { ApplyUpdatesResult } from '../../hooks/useActionPlan';

// Target of the modal: either an explicit slot list (single slot or grid
// selection) or an inclusive from/to range (may extend beyond the grid).
export interface ModalTarget {
  slots?: number[];
  fromSlot?: number;
  toSlot?: number;
}

function targetCount(target: ModalTarget): number {
  if (target.slots?.length) return target.slots.length;
  if (target.fromSlot !== undefined && target.toSlot !== undefined) {
    return target.toSlot - target.fromSlot + 1;
  }
  return 0;
}

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------

const formatDateTime = (iso: string): string => new Date(iso).toLocaleString();

const formatGwei = (v?: number): string => (v === undefined ? '—' : `${v.toLocaleString()} gwei`);

const weiToEth = (wei?: string): string => {
  if (!wei) return '—';
  const num = Number(wei);
  if (!Number.isFinite(num)) return wei;
  return `${(num / 1e18).toFixed(6)} ETH`;
};

// Status → bootstrap badge classes. warning/info need dark text.
const badgeClass = (variant: string): string =>
  `badge bg-${variant}${variant === 'warning' || variant === 'info' ? ' text-dark' : ''}`;

const BUILD_BADGES: Record<string, string> = {
  ready: 'success',
  failed: 'danger',
  skipped: 'secondary',
  started: 'primary',
  waiting_attributes: 'info',
  no_attributes: 'warning',
};

const PAYLOAD_STATUS_BADGES: Record<string, string> = {
  canonical: 'success',
  missed: 'danger',
  orphaned: 'danger',
  pending: 'secondary',
};

const BID_BADGES: Record<string, string> = {
  served: 'success',
  submitted: 'success',
  constructed: 'info',
  suppressed: 'secondary',
  failed: 'danger',
  cancelled: 'warning',
};

const SUBMISSION_BADGES: Record<string, string> = {
  accepted: 'success',
  received: 'info',
  failed: 'danger',
};

const REVEAL_BADGES: Record<string, string> = {
  published: 'success',
  failed: 'danger',
  suppressed: 'warning',
  skipped: 'secondary',
};

const StatusBadge: React.FC<{ map: Record<string, string>; status: string }> = ({ map, status }) => (
  <span className={badgeClass(map[status] || 'secondary')}>{status}</span>
);

// ---------------------------------------------------------------------------
// Artifact access (JSON opens the endpoint, SSZ downloads via blob)
// ---------------------------------------------------------------------------

async function downloadSSZ(url: string, filename: string): Promise<void> {
  const resp = await fetch(url, { headers: { Accept: 'application/octet-stream' } });
  if (!resp.ok) {
    throw new Error(`Artifact download failed: ${resp.status} ${resp.statusText}`);
  }

  const blob = await resp.blob();
  const objectUrl = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = objectUrl;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(objectUrl);
}

const ArtifactLinks: React.FC<{
  url: string;
  filename: string;
  onError: (message: string) => void;
}> = ({ url, filename, onError }) => (
  <span className="d-inline-flex gap-1">
    <a href={url} target="_blank" rel="noreferrer" className="btn btn-outline-secondary ap-artifact-btn">
      JSON
    </a>
    <button
      type="button"
      className="btn btn-outline-secondary ap-artifact-btn"
      onClick={() => {
        downloadSSZ(url, filename).catch((err) =>
          onError(err instanceof Error ? err.message : String(err))
        );
      }}
    >
      SSZ
    </button>
  </span>
);

// ---------------------------------------------------------------------------
// Edit form model
// ---------------------------------------------------------------------------

// 'unchanged' exists only in bulk mode (leave targeted slots as they are);
// 'inherit' clears the category back to the global baseline.
type FormMode = 'unchanged' | 'inherit' | 'custom' | 'disabled';

interface FieldDef {
  key: string;
  label: string;
  unit: 'ms' | 'gwei';
}

const BID_FIELDS: FieldDef[] = [
  { key: 'bid_start_time', label: 'Bid Start (rel. slot)', unit: 'ms' },
  { key: 'bid_end_time', label: 'Bid End (rel. slot)', unit: 'ms' },
  { key: 'bid_min_amount', label: 'Bid Min', unit: 'gwei' },
  { key: 'bid_increase', label: 'Bid Increase', unit: 'gwei' },
  { key: 'bid_interval', label: 'Bid Interval', unit: 'ms' },
  { key: 'bid_subsidy', label: 'Bid Subsidy', unit: 'gwei' },
  { key: 'bid_value_gwei', label: 'Bid Value Override', unit: 'gwei' },
];

const BUILDER_API_FIELDS: FieldDef[] = [
  { key: 'value_subsidy_gwei', label: 'Value Subsidy', unit: 'gwei' },
  { key: 'total_value_override_gwei', label: 'Total Value Override', unit: 'gwei' },
  { key: 'response_delay_ms', label: 'Response Delay', unit: 'ms' },
];

const REVEAL_FIELDS: FieldDef[] = [{ key: 'reveal_time_ms', label: 'Reveal Time (rel. slot)', unit: 'ms' }];

interface CategoryFormState {
  mode: FormMode;
  fields: Record<string, string>; // raw input values; empty = inherit
  ignoreMissingPrefs: boolean;
}

function initCategoryState(
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
    if (typeof value === 'number') {
      fields[def.key] = String(value);
    }
  }

  return {
    mode: category.mode,
    fields,
    ignoreMissingPrefs: raw['ignore_missing_prefs'] === true,
  };
}

function parseCategoryFields(
  name: string,
  defs: FieldDef[],
  state: CategoryFormState
): { values: Record<string, number>; error: string | null } {
  const values: Record<string, number> = {};

  for (const def of defs) {
    const raw = (state.fields[def.key] ?? '').trim();
    if (raw === '') continue;

    const num = Number(raw);
    if (!Number.isFinite(num) || !Number.isInteger(num)) {
      return { values, error: `${name}: ${def.label} must be an integer (${def.unit})` };
    }
    values[def.key] = num;
  }

  return { values, error: null };
}

type CategoryOutcome =
  | { kind: 'none' }
  | { kind: 'clear' }
  | { kind: 'replace'; obj: Record<string, unknown> }
  | { kind: 'set'; paths: Record<string, number | boolean | null> }
  | { kind: 'error'; error: string };

// resolveCategory turns one category form into its PlanUpdate contribution.
// Single-slot edits of an existing custom category use fine-grained `set`
// paths so unchanged sibling fields are never clobbered; mode switches send
// the full category object.
function resolveCategory(
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
        const paths: Record<string, number | boolean | null> = {};

        for (const def of defs) {
          const oldValue = typeof initial[def.key] === 'number' ? (initial[def.key] as number) : undefined;
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

const CategoryForm: React.FC<{
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
// Read-only frozen plan + result views
// ---------------------------------------------------------------------------

const KV: React.FC<{ label: string; children: React.ReactNode }> = ({ label, children }) => (
  <div className="col-6 col-md-4">
    <div className="config-item">
      <div className="config-item-label">{label}</div>
      <div className="config-item-value">{children}</div>
    </div>
  </div>
);

const FrozenPlanSection: React.FC<{ frozen: FrozenPlan }> = ({ frozen }) => (
  <div className="card mb-3">
    <div className="card-header py-1 d-flex flex-wrap align-items-center gap-2">
      <strong className="small">Applied Plan (frozen)</strong>
      <span className={badgeClass('secondary')}>{frozen.fork}</span>
      <span className="text-muted small ms-auto">frozen {formatDateTime(frozen.frozen_at)}</span>
    </div>
    <div className="card-body py-2">
      <div className="row g-2 mb-2">
        <KV label="Build">
          {frozen.build.build ? (
            <span className={badgeClass('success')}>build{frozen.build.forced ? ' (forced)' : ''}</span>
          ) : (
            <span className={badgeClass('secondary')}>skip ({frozen.build.skip_reason || '—'})</span>
          )}
        </KV>
        <KV label="Build Start">{frozen.build.build_start_time_ms} ms</KV>
      </div>

      <div className="section-header mb-1">Bid (p2p)</div>
      {frozen.bid ? (
        <div className="row g-2 mb-2">
          <KV label="Window">
            {frozen.bid.start_ms}–{frozen.bid.end_ms} ms
          </KV>
          <KV label="Interval">{frozen.bid.interval_ms} ms</KV>
          <KV label="Min / Increase">
            {formatGwei(frozen.bid.min_gwei)} / {formatGwei(frozen.bid.increase_gwei)}
          </KV>
          <KV label="Subsidy">{formatGwei(frozen.bid.subsidy_gwei)}</KV>
          <KV label="Value Override">{formatGwei(frozen.bid.value_gwei)}</KV>
          <KV label="Flags">
            {frozen.bid.forced && <span className={`${badgeClass('warning')} me-1`}>forced</span>}
            {frozen.bid.ignore_missing_prefs && (
              <span className={badgeClass('info')}>ignore missing prefs</span>
            )}
            {!frozen.bid.forced && !frozen.bid.ignore_missing_prefs && '—'}
          </KV>
        </div>
      ) : (
        <p className="text-muted small mb-2">suppressed for this slot</p>
      )}

      <div className="section-header mb-1">Builder API</div>
      {frozen.builder_api ? (
        <div className="row g-2 mb-2">
          <KV label="Subsidy">{formatGwei(frozen.builder_api.subsidy_gwei)}</KV>
          <KV label="Total Value Override">{formatGwei(frozen.builder_api.total_value_gwei)}</KV>
          <KV label="Response Delay">{frozen.builder_api.delay_ms ?? 0} ms</KV>
          {frozen.builder_api.forced && (
            <KV label="Flags">
              <span className={badgeClass('warning')}>forced</span>
            </KV>
          )}
        </div>
      ) : (
        <p className="text-muted small mb-2">suppressed for this slot</p>
      )}

      <div className="section-header mb-1">Reveal</div>
      {frozen.reveal ? (
        <div className="row g-2 mb-2">
          <KV label="Status">
            {frozen.reveal.suppressed ? (
              <span className={badgeClass('danger')}>suppressed</span>
            ) : (
              <span className={badgeClass('success')}>active</span>
            )}
          </KV>
          <KV label="Reveal Time">{frozen.reveal.reveal_time_ms} ms</KV>
          {frozen.reveal.bypass_deadline && (
            <KV label="Flags">
              <span className={badgeClass('warning')}>bypass deadline</span>
            </KV>
          )}
        </div>
      ) : (
        <p className="text-muted small mb-2">—</p>
      )}

      <div className="section-header mb-1">Raw Plan</div>
      {frozen.plan ? (
        <pre className="ap-raw-plan mb-0">{JSON.stringify(frozen.plan, null, 2)}</pre>
      ) : (
        <p className="text-muted small mb-0">No per-slot plan — global baseline applied.</p>
      )}
    </div>
  </div>
);

const ResultView: React.FC<{
  slot: number;
  result?: SlotResult;
  storedPlan?: SlotPlan;
  onArtifactError: (message: string) => void;
}> = ({ slot, result, storedPlan, onArtifactError }) => {
  if (!result) {
    return (
      <>
        <div className="alert alert-secondary small">No result recorded for this slot.</div>
        {storedPlan && (
          <>
            <div className="section-header mb-1">Stored Plan</div>
            <pre className="ap-raw-plan mb-0">{JSON.stringify(storedPlan, null, 2)}</pre>
          </>
        )}
      </>
    );
  }

  const baseUrl = `/api/buildoor/slot-results/${slot}`;
  const build = result.build;
  const payloadExists = build?.status === 'ready' || !!build?.block_hash;
  const reveals = result.reveal_attempts || [];
  const envelopeExists = reveals.some((r) => r.status === 'published' || r.status === 'failed');

  return (
    <>
      {result.applied_plan && <FrozenPlanSection frozen={result.applied_plan} />}

      {build && (
        <div className="card mb-3">
          <div className="card-header py-1 d-flex flex-wrap align-items-center gap-2">
            <strong className="small">Build</strong>
            <StatusBadge map={BUILD_BADGES} status={build.status} />
            {build.skip_reason && <span className="text-muted small">({build.skip_reason})</span>}
            {payloadExists && (
              <span className="ms-auto">
                <ArtifactLinks
                  url={`${baseUrl}/payload`}
                  filename={`slot-${slot}-payload.ssz`}
                  onError={onArtifactError}
                />
              </span>
            )}
          </div>
          <div className="card-body py-2">
            {build.error && <div className="alert alert-danger small py-1 px-2 mb-2">{build.error}</div>}
            <div className="row g-2">
              {build.block_hash && (
                <div className="col-12">
                  <div className="config-item">
                    <div className="config-item-label">Block Hash</div>
                    <div className="config-item-value font-monospace ap-break">{build.block_hash}</div>
                  </div>
                </div>
              )}
              <KV label="Value">{weiToEth(build.block_value_wei)}</KV>
              <KV label="Txs / Blobs">
                {build.num_transactions ?? 0} / {build.num_blobs ?? 0}
              </KV>
              <KV label="At">{formatDateTime(build.at)}</KV>
              {build.fee_recipient && (
                <div className="col-12">
                  <div className="config-item">
                    <div className="config-item-label">Fee Recipient</div>
                    <div className="config-item-value font-monospace ap-break">{build.fee_recipient}</div>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      )}

      {(result.bids?.length || 0) > 0 && (
        <div className="card mb-3">
          <div className="card-header py-1">
            <strong className="small">Bids</strong>
            {result.dropped_attempts?.['bids'] ? (
              <span className="text-muted small ms-2">
                (+{result.dropped_attempts['bids']} dropped)
              </span>
            ) : null}
          </div>
          <div className="table-responsive">
            <table className="table table-sm table-hover small mb-0">
              <thead>
                <tr>
                  <th>#</th>
                  <th>Status</th>
                  <th>Transport</th>
                  <th className="text-end">Value</th>
                  <th className="text-end">Exec Payment</th>
                  <th className="text-end">Competitor High</th>
                  <th>Artifact</th>
                  <th className="text-end">At</th>
                </tr>
              </thead>
              <tbody>
                {result.bids!.map((bid, i) => (
                  <tr key={i}>
                    <td>{i + 1}</td>
                    <td>
                      <StatusBadge map={BID_BADGES} status={bid.status} />
                      {bid.error && (
                        <i className="fas fa-triangle-exclamation text-danger ms-1" title={bid.error}></i>
                      )}
                    </td>
                    <td>{bid.transport}</td>
                    <td className="text-end font-monospace">{bid.total_value_gwei.toLocaleString()}</td>
                    <td className="text-end font-monospace">
                      {bid.execution_payment_gwei !== undefined
                        ? bid.execution_payment_gwei.toLocaleString()
                        : '—'}
                    </td>
                    <td className="text-end font-monospace">
                      {bid.competitor_high_gwei !== undefined
                        ? bid.competitor_high_gwei.toLocaleString()
                        : '—'}
                    </td>
                    <td>
                      {bid.artifact_index !== undefined ? (
                        <ArtifactLinks
                          url={`${baseUrl}/bids/${bid.artifact_index}`}
                          filename={`slot-${slot}-bid-${bid.artifact_index}.ssz`}
                          onError={onArtifactError}
                        />
                      ) : (
                        <span className="text-muted">—</span>
                      )}
                    </td>
                    <td className="text-end text-muted">{formatDateTime(bid.at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {(result.block_submissions?.length || 0) > 0 && (
        <div className="card mb-3">
          <div className="card-header py-1">
            <strong className="small">Block Submissions</strong>
          </div>
          <div className="table-responsive">
            <table className="table table-sm small mb-0">
              <thead>
                <tr>
                  <th>Dialect</th>
                  <th>Status</th>
                  <th>Error</th>
                  <th className="text-end">At</th>
                </tr>
              </thead>
              <tbody>
                {result.block_submissions!.map((sub, i) => (
                  <tr key={i}>
                    <td>{sub.dialect}</td>
                    <td>
                      <StatusBadge map={SUBMISSION_BADGES} status={sub.status} />
                    </td>
                    <td className="text-danger small">{sub.error || ''}</td>
                    <td className="text-end text-muted">{formatDateTime(sub.at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {(reveals.length > 0 || envelopeExists) && (
        <div className="card mb-3">
          <div className="card-header py-1 d-flex flex-wrap align-items-center gap-2">
            <strong className="small">Reveal Attempts</strong>
            {envelopeExists && (
              <span className="ms-auto">
                <ArtifactLinks
                  url={`${baseUrl}/envelope`}
                  filename={`slot-${slot}-envelope.ssz`}
                  onError={onArtifactError}
                />
              </span>
            )}
          </div>
          <div className="table-responsive">
            <table className="table table-sm small mb-0">
              <thead>
                <tr>
                  <th>Attempt</th>
                  <th>Status</th>
                  <th>Transport</th>
                  <th>Detail</th>
                  <th className="text-end">At</th>
                </tr>
              </thead>
              <tbody>
                {reveals.map((attempt, i) => (
                  <tr key={i}>
                    <td>{attempt.attempt}</td>
                    <td>
                      <StatusBadge map={REVEAL_BADGES} status={attempt.status} />
                    </td>
                    <td>{attempt.transport}</td>
                    <td className="small">
                      {attempt.skip_reason && <span className="text-muted">{attempt.skip_reason}</span>}
                      {attempt.error && <span className="text-danger"> {attempt.error}</span>}
                    </td>
                    <td className="text-end text-muted">{formatDateTime(attempt.at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {result.inclusion && (
        <div className="card mb-3">
          <div className="card-header py-1 d-flex align-items-center gap-2">
            <strong className="small">Inclusion</strong>
            <span className={badgeClass('success')}>{result.inclusion.source}</span>
            {result.inclusion.payload_status && (
              <span
                className={badgeClass(
                  PAYLOAD_STATUS_BADGES[result.inclusion.payload_status] || 'secondary'
                )}
                title="Canonical payload verdict from the next canonical block's committed parent hash (revised on reorgs)"
              >
                payload {result.inclusion.payload_status}
              </span>
            )}
          </div>
          <div className="card-body py-2">
            <div className="row g-2">
              <div className="col-12">
                <div className="config-item">
                  <div className="config-item-label">Block Hash</div>
                  <div className="config-item-value font-monospace ap-break">
                    {result.inclusion.block_hash}
                  </div>
                </div>
              </div>
              <KV label="Txs / Blobs">
                {result.inclusion.num_transactions} / {result.inclusion.num_blobs}
              </KV>
              <KV label="Value">{parseFloat(result.inclusion.value_eth).toFixed(6)} ETH</KV>
              <KV label="At">{formatDateTime(result.inclusion.timestamp)}</KV>
              {result.inclusion.payload_check_slot !== undefined && (
                <KV label="Verdict from slot">{String(result.inclusion.payload_check_slot)}</KV>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  );
};

// ---------------------------------------------------------------------------
// Modal
// ---------------------------------------------------------------------------

interface SlotEditModalProps {
  target: ModalTarget;
  plans: Record<number, SlotPlan>;
  results: Record<number, SlotResult>;
  currentSlot: number;
  canEdit: boolean;
  applyUpdates: (updates: PlanUpdate[]) => Promise<ApplyUpdatesResult>;
  onClose: () => void;
}

export const SlotEditModal: React.FC<SlotEditModalProps> = ({
  target,
  plans,
  results,
  currentSlot,
  canEdit,
  applyUpdates,
  onClose,
}) => {
  const count = targetCount(target);
  const isSingle = count === 1;
  const singleSlot = isSingle ? (target.slots?.[0] ?? target.fromSlot ?? 0) : 0;
  const isPastSingle = isSingle && singleSlot <= currentSlot;
  const bulk = !isSingle;

  const initialPlan = isSingle ? plans[singleSlot] : undefined;

  const [bidState, setBidState] = useState<CategoryFormState>(() =>
    initCategoryState(initialPlan?.bid, BID_FIELDS, bulk)
  );
  const [apiState, setApiState] = useState<CategoryFormState>(() =>
    initCategoryState(initialPlan?.builder_api, BUILDER_API_FIELDS, bulk)
  );
  const [revealState, setRevealState] = useState<CategoryFormState>(() =>
    initCategoryState(initialPlan?.reveal, REVEAL_FIELDS, bulk)
  );

  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [conflictError, setConflictError] = useState<string | null>(null);
  const [artifactError, setArtifactError] = useState<string | null>(null);

  const targetLabel = isSingle
    ? `Slot ${singleSlot}`
    : target.slots?.length
      ? `${count} slots selected`
      : `Slots ${target.fromSlot}–${target.toSlot} (${count} slots)`;

  const applyTargets = (update: PlanUpdate) => {
    if (target.slots?.length) {
      update.slots = target.slots;
    } else {
      update.from_slot = target.fromSlot;
      update.to_slot = target.toSlot;
    }
  };

  const runUpdate = async (update: PlanUpdate) => {
    setFormError(null);
    setConflictError(null);
    setSaving(true);

    const result = await applyUpdates([update]);
    setSaving(false);

    if (result.ok) {
      onClose();
    } else if (result.conflict) {
      setConflictError(result.error || 'Slot already frozen or past — refresh the view.');
    } else {
      setFormError(result.error || 'Update failed');
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setConflictError(null);

    const update: PlanUpdate = {};
    applyTargets(update);

    const updateRec = update as unknown as Record<string, unknown>;
    const setPaths: Record<string, number | string | boolean | null> = {};

    const outcomes: Array<[string, CategoryOutcome]> = [
      ['bid', resolveCategory('bid', BID_FIELDS, bidState, initialPlan?.bid as unknown as Record<string, unknown> | undefined, isSingle, true)],
      ['builder_api', resolveCategory('builder_api', BUILDER_API_FIELDS, apiState, initialPlan?.builder_api as unknown as Record<string, unknown> | undefined, isSingle, false)],
      ['reveal', resolveCategory('reveal', REVEAL_FIELDS, revealState, initialPlan?.reveal as unknown as Record<string, unknown> | undefined, isSingle, false)],
    ];

    let hasChange = false;
    for (const [name, outcome] of outcomes) {
      switch (outcome.kind) {
        case 'error':
          setFormError(outcome.error);
          return;
        case 'clear':
          updateRec[name] = null;
          hasChange = true;
          break;
        case 'replace':
          updateRec[name] = outcome.obj;
          hasChange = true;
          break;
        case 'set':
          Object.assign(setPaths, outcome.paths);
          hasChange = true;
          break;
        case 'none':
          break;
      }
    }

    if (Object.keys(setPaths).length > 0) {
      update.set = setPaths;
    }

    if (!hasChange) {
      setFormError('No changes to save');
      return;
    }

    await runUpdate(update);
  };

  const handleDelete = async () => {
    const update: PlanUpdate = { delete: true };
    applyTargets(update);
    await runUpdate(update);
  };

  const showEditForm = !isPastSingle;
  const formDisabled = !canEdit || saving;
  const deletable = bulk || !!initialPlan;

  return (
    <>
      <div className="modal fade show d-block" tabIndex={-1} role="dialog" aria-modal="true">
        <div className="modal-dialog modal-lg modal-dialog-scrollable">
          <div className="modal-content">
            <div className="modal-header py-2">
              <h5 className="modal-title">
                {isPastSingle ? `${targetLabel} — Result` : `Edit Plan — ${targetLabel}`}
              </h5>
              <button type="button" className="btn-close" aria-label="Close" onClick={onClose}></button>
            </div>

            <div className="modal-body">
              {conflictError && (
                <div className="alert alert-warning small py-2">
                  <i className="fas fa-lock me-1"></i>
                  {conflictError}
                </div>
              )}
              {formError && <div className="alert alert-danger small py-2">{formError}</div>}
              {artifactError && (
                <div className="alert alert-danger small py-2 d-flex align-items-center">
                  <span className="me-auto">{artifactError}</span>
                  <button
                    type="button"
                    className="btn-close"
                    aria-label="Dismiss"
                    onClick={() => setArtifactError(null)}
                  ></button>
                </div>
              )}

              {isPastSingle ? (
                <ResultView
                  slot={singleSlot}
                  result={results[singleSlot]}
                  storedPlan={plans[singleSlot]}
                  onArtifactError={setArtifactError}
                />
              ) : (
                <form id="ap-edit-form" onSubmit={handleSave}>
                  {!canEdit && (
                    <div className="alert alert-info small py-2">
                      <i className="fas fa-info-circle me-1"></i>
                      Login required to edit per-slot plans.
                    </div>
                  )}
                  {bulk && (
                    <div className="form-text mb-2">
                      Changes apply to all {count} targeted slots. &quot;unchanged&quot; leaves a
                      category as it is per slot; &quot;inherit (clear)&quot; removes it.
                    </div>
                  )}
                  <CategoryForm
                    title="Bid (p2p)"
                    bulk={bulk}
                    state={bidState}
                    fields={BID_FIELDS}
                    disabled={formDisabled}
                    showIgnorePrefs
                    onChange={setBidState}
                  />
                  <CategoryForm
                    title="Builder API"
                    bulk={bulk}
                    state={apiState}
                    fields={BUILDER_API_FIELDS}
                    disabled={formDisabled}
                    onChange={setApiState}
                  />
                  <CategoryForm
                    title="Reveal"
                    bulk={bulk}
                    state={revealState}
                    fields={REVEAL_FIELDS}
                    disabled={formDisabled}
                    onChange={setRevealState}
                  />
                </form>
              )}
            </div>

            <div className="modal-footer py-2">
              {showEditForm && canEdit && deletable && (
                <button
                  type="button"
                  className="btn btn-sm btn-outline-danger me-auto"
                  disabled={saving}
                  onClick={handleDelete}
                >
                  <i className="fas fa-trash me-1"></i>
                  {bulk ? 'Delete plans' : 'Delete plan'}
                </button>
              )}
              <button type="button" className="btn btn-sm btn-secondary" onClick={onClose}>
                {showEditForm ? 'Cancel' : 'Close'}
              </button>
              {showEditForm && canEdit && (
                <button type="submit" form="ap-edit-form" className="btn btn-sm btn-primary" disabled={saving}>
                  {saving && <span className="spinner-border spinner-border-sm me-1"></span>}
                  Save
                </button>
              )}
            </div>
          </div>
        </div>
      </div>
      <div className="modal-backdrop fade show"></div>
    </>
  );
};
