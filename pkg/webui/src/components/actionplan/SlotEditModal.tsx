import React, { useState } from 'react';
import type {
  FrozenPlan,
  PlanUpdate,
  SlotPlan,
  SlotResult,
} from '../../types';
import type { ApplyUpdatesResult } from '../../hooks/useActionPlan';
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
  type CategoryOutcome,
} from './planForms';

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
          {frozen.build.reorg_parent_payload && (
            <span className={`ms-1 ${badgeClass('warning')}`} title="Built on the grandparent (n-2) payload">
              reorg parent
            </span>
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
          <KV label="Gate">{frozen.reveal.gate_mode}</KV>
          <KV label="Reveal Time">{frozen.reveal.reveal_time_ms} ms</KV>
          {frozen.reveal.gate_mode !== 'time' && (
            <KV label="Vote Threshold">{frozen.reveal.vote_threshold_pct}%</KV>
          )}
          <KV label="Broadcast Validation">{frozen.reveal.broadcast_validation}</KV>
          <KV label="Retries">
            {frozen.reveal.max_attempts} × {frozen.reveal.retry_interval_ms} ms
          </KV>
          {frozen.reveal.bypass_deadline && (
            <KV label="Flags">
              <span className={badgeClass('warning')}>bypass deadline</span>
            </KV>
          )}
        </div>
      ) : (
        <p className="text-muted small mb-2">—</p>
      )}

      {frozen.transforms &&
        (frozen.transforms.payload || frozen.transforms.bid || frozen.transforms.envelope) && (
          <>
            <div className="section-header mb-1">Transforms (jq)</div>
            <div className="mb-2">
              {(['payload', 'bid', 'envelope'] as const).map((k) =>
                frozen.transforms?.[k] ? (
                  <div key={k} className="mb-1">
                    <div className="ap-transform-io-label">{k}</div>
                    <pre className="ap-transform-io mb-0">{frozen.transforms[k]}</pre>
                  </div>
                ) : null
              )}
            </div>
          </>
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
              <KV label="Contents">
                {build.num_transactions ?? 0} txs, {build.num_blobs ?? 0} blobs,{' '}
                {build.num_withdrawals ?? 0} wdrls, {build.num_execution_requests ?? 0} reqs
              </KV>
              {build.block_number !== undefined && <KV label="Block #">{build.block_number}</KV>}
              {build.gas_used !== undefined && (
                <KV label="Gas">
                  {build.gas_used.toLocaleString()} / {(build.gas_limit ?? 0).toLocaleString()}
                </KV>
              )}
              {build.base_fee_per_gas && <KV label="Base Fee">{build.base_fee_per_gas} wei</KV>}
              {build.blob_gas_used !== undefined && build.blob_gas_used > 0 && (
                <KV label="Blob Gas">
                  {build.blob_gas_used.toLocaleString()} (excess {(build.excess_blob_gas ?? 0).toLocaleString()})
                </KV>
              )}
              {build.timestamp !== undefined && <KV label="Timestamp">{build.timestamp}</KV>}
              {build.extra_data && <KV label="Extra Data">{build.extra_data}</KV>}
              <KV label="At">{formatDateTime(build.at)}</KV>
              {build.parent_hash && (
                <div className="col-12">
                  <div className="config-item">
                    <div className="config-item-label">Parent Hash</div>
                    <div className="config-item-value font-monospace ap-break">{build.parent_hash}</div>
                  </div>
                </div>
              )}
              {build.state_root && (
                <div className="col-12">
                  <div className="config-item">
                    <div className="config-item-label">State Root</div>
                    <div className="config-item-value font-monospace ap-break">{build.state_root}</div>
                  </div>
                </div>
              )}
              {build.receipts_root && (
                <div className="col-12">
                  <div className="config-item">
                    <div className="config-item-label">Receipts Root</div>
                    <div className="config-item-value font-monospace ap-break">{build.receipts_root}</div>
                  </div>
                </div>
              )}
              {build.prev_randao && (
                <div className="col-12">
                  <div className="config-item">
                    <div className="config-item-label">Prev Randao</div>
                    <div className="config-item-value font-monospace ap-break">{build.prev_randao}</div>
                  </div>
                </div>
              )}
              {build.fee_recipient && (
                <div className="col-12">
                  <div className="config-item">
                    <div className="config-item-label">Fee Recipient</div>
                    <div className="config-item-value font-monospace ap-break">{build.fee_recipient}</div>
                  </div>
                </div>
              )}
            </div>
            {build.attributes && (
              <>
                <div className="section-header mt-3 mb-1">Payload Attributes</div>
                <div className="row g-2">
                  <KV label="Proposer">{build.attributes.proposer_index}</KV>
                  <KV label="Parent Block #">{build.attributes.parent_block_number}</KV>
                  <KV label="Timestamp">{build.attributes.timestamp}</KV>
                  {build.attributes.target_gas_limit !== undefined && build.attributes.target_gas_limit > 0 && (
                    <KV label="Target Gas Limit">{build.attributes.target_gas_limit.toLocaleString()}</KV>
                  )}
                  <KV label="Contents">
                    {build.attributes.num_withdrawals} wdrls
                    {build.attributes.num_inclusion_list_txs ? `, ${build.attributes.num_inclusion_list_txs} IL txs` : ''}
                  </KV>
                  <div className="col-12">
                    <div className="config-item">
                      <div className="config-item-label">Parent Hash</div>
                      <div className="config-item-value font-monospace ap-break">{build.attributes.parent_block_hash}</div>
                    </div>
                  </div>
                  <div className="col-12">
                    <div className="config-item">
                      <div className="config-item-label">Parent Root</div>
                      <div className="config-item-value font-monospace ap-break">{build.attributes.parent_block_root}</div>
                    </div>
                  </div>
                  {build.attributes.parent_beacon_block_root && (
                    <div className="col-12">
                      <div className="config-item">
                        <div className="config-item-label">Beacon Parent Root</div>
                        <div className="config-item-value font-monospace ap-break">{build.attributes.parent_beacon_block_root}</div>
                      </div>
                    </div>
                  )}
                  <div className="col-12">
                    <div className="config-item">
                      <div className="config-item-label">Prev Randao</div>
                      <div className="config-item-value font-monospace ap-break">{build.attributes.prev_randao}</div>
                    </div>
                  </div>
                  <div className="col-12">
                    <div className="config-item">
                      <div className="config-item-label">Suggested Fee Recipient</div>
                      <div className="config-item-value font-monospace ap-break">{build.attributes.suggested_fee_recipient}</div>
                    </div>
                  </div>
                </div>
              </>
            )}
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
                  <th>Block Hash</th>
                  <th className="text-end">Gas Limit</th>
                  <th className="text-end">Blobs</th>
                  <th>Artifact</th>
                  <th className="text-end">At</th>
                </tr>
              </thead>
              <tbody>
                {result.bids!.map((bid, i) => (
                  <tr
                    key={i}
                    title={[
                      bid.parent_block_hash ? `parent hash: ${bid.parent_block_hash}` : '',
                      bid.parent_block_root ? `parent root: ${bid.parent_block_root}` : '',
                      bid.prev_randao ? `prev randao: ${bid.prev_randao}` : '',
                      bid.fee_recipient ? `fee recipient: ${bid.fee_recipient}` : '',
                      bid.builder_index !== undefined ? `builder index: ${bid.builder_index}` : ''
                    ].filter(Boolean).join('\n')}
                  >
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
                    <td className="font-monospace">
                      {bid.block_hash ? `${bid.block_hash.slice(0, 10)}…` : '—'}
                    </td>
                    <td className="text-end font-monospace">
                      {bid.gas_limit !== undefined && bid.gas_limit > 0 ? bid.gas_limit.toLocaleString() : '—'}
                    </td>
                    <td className="text-end font-monospace">
                      {bid.num_blob_commitments ?? 0}
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
                  <th className="text-end">Started</th>
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
                    <td className="text-end text-muted">
                      {attempt.started_at ? formatDateTime(attempt.started_at) : '—'}
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

  // The effective plan seeds the form; only a STORED plan is a diff base. A
  // rule-derived plan lives nowhere, so saving it writes full categories and
  // detaches the slot from the rule.
  const effectivePlan = isSingle ? plans[singleSlot] : undefined;
  const rulePlan = effectivePlan?.rule_id ? effectivePlan : undefined;
  const initialPlan = rulePlan ? undefined : effectivePlan;

  const [bidState, setBidState] = useState<CategoryFormState>(() =>
    initCategoryState(effectivePlan?.bid, BID_FIELDS, bulk)
  );
  const [apiState, setApiState] = useState<CategoryFormState>(() =>
    initCategoryState(effectivePlan?.builder_api, BUILDER_API_FIELDS, bulk)
  );
  const [revealState, setRevealState] = useState<CategoryFormState>(() =>
    initCategoryState(effectivePlan?.reveal, REVEAL_FIELDS, bulk)
  );
  // Build is a modeless single-flag category: tri-state in bulk, on/off single.
  const [buildReorg, setBuildReorg] = useState<BuildFlagMode>(() =>
    bulk ? 'unchanged' : effectivePlan?.build?.reorg_parent_payload ? 'on' : 'off'
  );

  // Per-candidate build policy overrides ('' = inherit the global policy).
  // Single-slot editing only: a bulk category replace would clobber unrelated
  // build settings on every targeted slot.
  const [buildCandidates, setBuildCandidates] = useState<Record<string, string>>(() => ({
    parent_full: (!bulk && effectivePlan?.build?.candidates?.parent_full) || '',
    parent_empty: (!bulk && effectivePlan?.build?.candidates?.parent_empty) || '',
    grandparent_full: (!bulk && effectivePlan?.build?.candidates?.grandparent_full) || '',
    grandparent_empty: (!bulk && effectivePlan?.build?.candidates?.grandparent_empty) || '',
  }));

  // Transforms (modeless jq expressions). In bulk mode we start empty and only
  // send the ones the operator fills in.
  const [transforms, setTransforms] = useState<TransformState>(() => ({
    payload: (!bulk && effectivePlan?.transforms?.payload) || '',
    bid: (!bulk && effectivePlan?.transforms?.bid) || '',
    envelope: (!bulk && effectivePlan?.transforms?.envelope) || '',
  }));

  // Per-slot opt-out from every recurring rule. In bulk mode it is write-only
  // (checked applies it to all targets, unchecked leaves each slot as it is).
  const [ignoreRules, setIgnoreRules] = useState(initialPlan?.ignore_rules === true);

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

    // Build (modeless). Candidate policy overrides need a full category
    // replace (map-valued member); the plain reorg flag alone keeps the
    // fine-grained set path (bulk mode only edits the flag).
    const candidateEntries = Object.entries(buildCandidates).filter(([, mode]) => mode !== '');
    const initialCandidates = initialPlan?.build?.candidates ?? {};
    const candidatesChanged = isSingle && (
      candidateEntries.length !== Object.keys(initialCandidates).length ||
      candidateEntries.some(([key, mode]) => initialCandidates[key] !== mode)
    );

    if (candidatesChanged) {
      const buildObj: Record<string, unknown> = {};
      const wantReorg = buildReorg === 'on';

      if (wantReorg) buildObj.reorg_parent_payload = true;

      if (candidateEntries.length > 0) {
        buildObj.candidates = Object.fromEntries(candidateEntries);
      }

      updateRec['build'] = Object.keys(buildObj).length > 0 ? buildObj : null;
      hasChange = true;
    } else if (buildReorg !== 'unchanged') {
      const initialReorg = initialPlan?.build?.reorg_parent_payload === true;
      const wantOn = buildReorg === 'on';
      // In single mode skip a no-op; in bulk always write so every targeted
      // slot converges (setting false drops the build category server-side).
      if (!isSingle || wantOn !== initialReorg) {
        setPaths['build.reorg_parent_payload'] = wantOn;
        hasChange = true;
      }
    }

    // Transforms (modeless jq expressions) as fine-grained set paths. Single
    // mode sends changed fields (empty clears); bulk sends only filled fields
    // (empty leaves each slot as it is), matching the other bulk categories.
    (['payload', 'bid', 'envelope'] as const).forEach((key) => {
      const val = transforms[key].trim();
      if (bulk) {
        if (val !== '') {
          setPaths[`transforms.${key}`] = val;
          hasChange = true;
        }
        return;
      }
      const old = (initialPlan?.transforms?.[key] || '').trim();
      if (val !== old) {
        setPaths[`transforms.${key}`] = val; // empty string clears (dropped server-side)
        hasChange = true;
      }
    });

    if (bulk ? ignoreRules : ignoreRules !== (initialPlan?.ignore_rules === true)) {
      update.ignore_rules = ignoreRules;
      hasChange = true;
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
                  {rulePlan && (
                    <div className="alert alert-secondary small py-2">
                      <i className="fas fa-repeat me-1"></i>
                      This slot has no plan of its own — it follows recurring rule{' '}
                      <strong>{rulePlan.rule_id}</strong>, whose values are pre-filled below. Saving
                      stores them as an explicit plan for this slot, detaching it from the rule.
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
                  <BuildForm
                    bulk={bulk}
                    value={buildReorg}
                    disabled={formDisabled}
                    onChange={setBuildReorg}
                  />
                  {!bulk && (
                    <div className="mb-3">
                      <div className="section-header mb-1">Build candidates</div>
                      <div className="form-text mt-0 mb-1">
                        Which candidate payloads the slot builds (parent/grandparent block,
                        full/empty parent payload). Inherit uses the global policy.
                      </div>
                      {Object.entries(buildCandidates).map(([key, mode]) => (
                        <div className="row g-2 align-items-center mb-1" key={key}>
                          <div className="col-6 small text-secondary">{key}</div>
                          <div className="col-6">
                            <select
                              className="form-select form-select-sm"
                              value={mode}
                              disabled={formDisabled}
                              onChange={(e) => setBuildCandidates(prev => ({
                                ...prev, [key]: e.target.value
                              }))}
                            >
                              <option value="">inherit</option>
                              <option value="auto">auto</option>
                              <option value="always">always</option>
                              <option value="never">never</option>
                            </select>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                  <div className="mb-3">
                    <div className="section-header mb-1">Recurring rules</div>
                    <div className="form-check">
                      <input
                        className="form-check-input"
                        type="checkbox"
                        id="ap-ignore-rules"
                        checked={ignoreRules}
                        disabled={formDisabled}
                        onChange={(e) => setIgnoreRules(e.target.checked)}
                      />
                      <label className="form-check-label small" htmlFor="ap-ignore-rules">
                        Ignore recurring rules
                        {bulk && ' (applies to all targeted slots; leave unchecked to keep each as it is)'}
                      </label>
                    </div>
                    <div className="form-text mt-0">
                      Runs the slot on the plain global baseline even when a rule matches it. Any
                      category set above already overrides the rule on its own.
                    </div>
                  </div>
                  <TransformEditor
                    bulk={bulk}
                    value={transforms}
                    disabled={formDisabled}
                    sampleSlot={isSingle ? singleSlot : undefined}
                    onChange={setTransforms}
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
