import { useLocalBuildStatus } from '../../hooks/useLocalBuildStatus';
import React, { useEffect, useMemo, useState } from 'react';
import type {
  AttributesSnapshot,
  BuildRecord,
  BuildTargetRecord,
  FrozenPlan,
  PayloadRecord,
  PlanUpdate,
  SelectionRecord,
  SlotPlan,
  SlotResult,
} from '../../types';
import { buildLabel, payloadArtifactUrl, primaryTargetRecord, sameBuild } from '../../buildTargets';
import type { ApplyUpdatesResult } from '../../hooks/useActionPlan';
import { TransformEditor, type TransformState } from './TransformEditor';
import {
  BID_FIELDS,
  BUILDER_API_FIELDS,
  REVEAL_FIELDS,
  BID_CATEGORY_HINT,
  BUILDER_API_CATEGORY_HINT,
  REVEAL_CATEGORY_HINT,
  CANDIDATE_KEY_HINTS,
  BuildForm,
  LocalBuildForm,
  HiddenTransactionsNote,
  hiddenTransactionsError,
  initLocalBuildState,
  localBuildPlanFromState,
  type LocalBuildFormState,
  CategoryForm,
  initCategoryState,
  resolveCategory,
  type BuildFlagMode,
  EMPTY_LOCAL_BUILD_STATE,
  InclusionListForm,
  CandidatesForm,
  initInclusionListState,
  inclusionListPlanFromState,
  inclusionListError,
  type InclusionListFormState,
  type CategoryFormState,
  type CategoryOutcome,
} from './planForms';
import { InfoHint } from '../InfoHint';
import { Tabs } from '../ui/Tabs';
import { Tag } from '../ui/Tag';
import { ArtifactButtons } from '../ArtifactButtons';
import { apiPath } from '../../utils/api';
import { useEventStream } from '../../hooks/useEventStream';
import { CopyableHash } from '../CopyableHash';
import { decodeExtraData, slotStartTime } from '../../utils';

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
  building: 'primary',
  waiting_attributes: 'info',
  no_attributes: 'warning',
};

const TX_PLAN_BADGES: Record<string, string> = {
  pending: 'secondary',
  match: 'success',
  mismatch: 'danger',
  block_not_found: 'danger',
  not_included: 'warning',
  missed: 'danger',
  orphaned: 'danger',
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
// Past slot result: the outcome first (what happened, stage by stage), then
// the builds, the bids and the reveal attempts, with the raw payload fields,
// the payload attributes and the applied plan folded away.
// ---------------------------------------------------------------------------

// formatOffset renders a time relative to the slot start ("−3.00s", "+412ms").
const formatOffset = (ms: number): string => {
  const sign = ms < 0 ? '−' : '+';
  const abs = Math.abs(Math.round(ms));
  return abs < 1000 ? `${sign}${abs}ms` : `${sign}${(abs / 1000).toFixed(2)}s`;
};

const formatDuration = (ms: number): string =>
  ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(2)}s`;

// SlotClock places the records' wall-clock times on the slot's own clock, as
// the slot graph does.
interface SlotClock {
  start?: number; // slot start, unix ms
}

const useSlotClock = (slot: number): SlotClock => {
  const { chainInfo } = useEventStream();
  return useMemo(
    () => ({ start: chainInfo ? slotStartTime(chainInfo, slot) : undefined }),
    [chainInfo, slot]
  );
};

// At renders a record time relative to the slot start, the absolute time on
// hover.
const At: React.FC<{ clock: SlotClock; at?: string }> = ({ clock, at }) => {
  if (!at) return <span className="text-muted">—</span>;
  const time = Date.parse(at);
  return (
    <span className="ap-time" title={new Date(time).toLocaleString()}>
      {clock.start !== undefined ? formatOffset(time - clock.start) : new Date(time).toLocaleTimeString()}
    </span>
  );
};

const KV: React.FC<{ label: string; hint?: React.ReactNode; wide?: boolean; children: React.ReactNode }> = ({
  label,
  hint,
  wide,
  children,
}) => (
  <div className={wide ? 'col-12' : 'col-6'}>
    <div className="config-item">
      <div className="config-item-label">
        {label}
        {hint && <InfoHint>{hint}</InfoHint>}
      </div>
      <div className="config-item-value">{children}</div>
    </div>
  </div>
);

// HashList is a compact label/hash list; each hash copies on click.
const HashList: React.FC<{ rows: [string, string | undefined][] }> = ({ rows }) => (
  <dl className="ap-hashes mb-0">
    {rows
      .filter(([, value]) => !!value)
      .map(([label, value]) => (
        <React.Fragment key={label}>
          <dt>{label}</dt>
          <dd className="font-monospace">
            <CopyableHash value={value!} chars={12} />
          </dd>
        </React.Fragment>
      ))}
  </dl>
);

// Fold is a collapsible section of the result view.
const Fold: React.FC<{
  title: React.ReactNode;
  extra?: React.ReactNode;
  open?: boolean;
  children: React.ReactNode;
}> = ({ title, extra, open, children }) => (
  <details className="ap-fold" open={open}>
    <summary>
      <span className="ap-fold-title">{title}</span>
      {extra && <span className="ap-fold-extra">{extra}</span>}
    </summary>
    <div className="ap-fold-body">{children}</div>
  </details>
);

// ---------------------------------------------------------------------------
// Outcome: one row per stage of the slot
// ---------------------------------------------------------------------------

interface OutcomeRow {
  stage: string;
  variant: string; // bootstrap color of the status badge
  status: string;
  detail?: React.ReactNode;
}

const sumBy = <T,>(items: T[], pick: (item: T) => number): number =>
  items.reduce((max, item) => Math.max(max, pick(item)), 0);

const outcomeRows = (result: SlotResult, clock: SlotClock, primary?: BuildTargetRecord): OutcomeRow[] => {
  const rows: OutcomeRow[] = [];
  const build = result.build;
  const frozen = result.applied_plan;

  // Build: the primary target's pick.
  if (build) {
    const picks = primary?.selections ?? [];
    const pick = primary?.status === 'selected' ? picks[picks.length - 1] : undefined;
    const payload = pick ? result.payloads?.[pick.payload_hash] : undefined;
    const targets = build.targets?.length ?? 0;

    rows.push({
      stage: 'Build',
      variant: BUILD_BADGES[build.status] || 'secondary',
      status: build.status,
      detail: build.skip_reason ? (
        build.skip_reason
      ) : payload ? (
        <>
          picked the <strong>{buildLabel(payload.build)}</strong> payload · {weiToEth(payload.block_value_wei)} ·{' '}
          {payload.num_transactions} txs, {payload.num_blobs ?? 0} blobs
          {targets > 1 && <span className="text-muted"> · {targets} parents built</span>}
        </>
      ) : (
        build.error
      ),
    });
  }

  // Bids, per transport.
  const bids = result.bids ?? [];
  const p2p = bids.filter((bid) => bid.transport === 'p2p');
  const api = bids.filter((bid) => bid.transport !== 'p2p');

  if (p2p.length > 0) {
    const sent = p2p.filter((bid) => bid.status === 'submitted');
    const competitor = sumBy(p2p, (bid) => bid.competitor_high_gwei ?? 0);
    rows.push({
      stage: 'p2p bids',
      variant: sent.length > 0 ? 'success' : 'danger',
      status: sent.length > 0 ? `${sent.length} sent` : 'none sent',
      detail: (
        <>
          top {sumBy(sent, (bid) => bid.total_value_gwei).toLocaleString()} gwei
          {competitor > 0 && <> · competitor {competitor.toLocaleString()} gwei</>}
          {p2p.length > sent.length && <span className="text-muted"> · {p2p.length - sent.length} not sent</span>}
        </>
      ),
    });
  }

  if (api.length > 0) {
    const served = api.filter((bid) => bid.status === 'served');
    rows.push({
      stage: 'Builder API',
      variant: served.length > 0 ? 'success' : BID_BADGES[api[api.length - 1].status] || 'secondary',
      status: served.length > 0 ? `${served.length}× served` : api[api.length - 1].status,
      detail: served.length > 0 && (
        <>
          {sumBy(served, (bid) => bid.total_value_gwei).toLocaleString()} gwei at <At clock={clock} at={served[0].at} />
        </>
      ),
    });
  }

  if (bids.length === 0 && build?.status !== 'skipped') {
    rows.push({
      stage: 'Bids',
      variant: 'secondary',
      status: 'none',
      detail: [
        frozen && !frozen.bid ? 'p2p bidding off for this slot' : 'no p2p bid',
        frozen && !frozen.builder_api ? 'Builder API off' : 'no Builder API request',
      ].join(' · '),
    });
  }

  // Blocks the proposer submitted through the Builder API.
  const submissions = result.block_submissions ?? [];
  if (submissions.length > 0) {
    const last = submissions[submissions.length - 1];
    rows.push({
      stage: 'Submission',
      variant: SUBMISSION_BADGES[last.status] || 'secondary',
      status: last.status,
      detail: (
        <>
          {last.dialect} block,{' '}
          {submissions.map((sub, i) => (
            <span key={i}>
              {i > 0 && ', '}
              {sub.status} <At clock={clock} at={sub.at} />
            </span>
          ))}
          {last.error && <div className="text-danger">{last.error}</div>}
        </>
      ),
    });
  }

  // Inclusion and the payload verdict.
  const inclusion = result.inclusion;
  if (inclusion) {
    rows.push({
      stage: 'Inclusion',
      variant: PAYLOAD_STATUS_BADGES[inclusion.payload_status ?? 'pending'] || 'secondary',
      status: inclusion.payload_status ? `payload ${inclusion.payload_status}` : 'won',
      detail: (
        <>
          won via {inclusion.source} · {parseFloat(inclusion.value_eth).toFixed(6)} ETH · {inclusion.num_transactions} txs,{' '}
          {inclusion.num_blobs} blobs ·{' '}
          <span className="font-monospace">
            <CopyableHash value={inclusion.block_hash} chars={6} />
          </span>
          {inclusion.payload_check_slot !== undefined && (
            <span className="text-muted"> · verdict from slot {String(inclusion.payload_check_slot)}</span>
          )}
        </>
      ),
    });
  } else if (bids.some((bid) => bid.status === 'submitted' || bid.status === 'served')) {
    rows.push({ stage: 'Inclusion', variant: 'secondary', status: 'not won' });
  }

  // Reveal (Gloas): the last attempt decides.
  const reveals = result.reveal_attempts ?? [];
  if (reveals.length > 0) {
    const last = reveals[reveals.length - 1];
    rows.push({
      stage: 'Reveal',
      variant: REVEAL_BADGES[last.status] || 'secondary',
      status: last.status,
      detail: (
        <>
          {last.skip_reason ?? last.transport} at <At clock={clock} at={last.at} />
          {reveals.length > 1 && <span className="text-muted"> · {reveals.length} attempts</span>}
          {last.error && <div className="text-danger">{last.error}</div>}
        </>
      ),
    });
  }

  // Post-inclusion check of a locally built payload.
  const plan = result.tx_plan;
  if (plan) {
    rows.push({
      stage: 'Tx plan',
      variant: TX_PLAN_BADGES[plan.status] || 'secondary',
      status: plan.status,
      detail: (
        <>
          {plan.expected_count} planned ({plan.tx_source})
          {plan.included_count !== undefined && plan.status !== 'pending' && `, ${plan.included_count} in block`}
          {(plan.first_mismatch ?? -1) >= 0 && `, first mismatch at ${plan.first_mismatch}`}
          {plan.detail && plan.status !== 'match' && <div className="text-danger">{plan.detail}</div>}
        </>
      ),
    });
  }

  return rows;
};

const OutcomeCard: React.FC<{ rows: OutcomeRow[]; envelope?: React.ReactNode }> = ({ rows, envelope }) => (
  <div className="card mb-3">
    <div className="card-header py-1 d-flex align-items-center gap-2">
      <strong className="small">
        Outcome
        <InfoHint>
          What happened in the slot, stage by stage: the payload picked for the consumers, the bids
          per transport, the block the proposer submitted (Builder API), the inclusion with the
          payload verdict (canonical, missed, orphaned), the reveal and the tx plan check. Times are
          relative to the slot start.
        </InfoHint>
      </strong>
      {envelope && <span className="ms-auto">{envelope}</span>}
    </div>
    <div className="card-body py-2">
      <dl className="ap-outcome mb-0">
        {rows.map((row) => (
          <React.Fragment key={row.stage}>
            <dt>{row.stage}</dt>
            <dd>
              <span className={badgeClass(row.variant)}>{row.status}</span>
              {row.detail && <span className="ap-outcome-detail">{row.detail}</span>}
            </dd>
          </React.Fragment>
        ))}
      </dl>
    </div>
  </div>
);

// ---------------------------------------------------------------------------
// Builds: one table per build target, a row per build, each build with its
// own payload (by block hash)
// ---------------------------------------------------------------------------

const TARGET_BADGES: Record<string, string> = {
  selected: 'success',
  building: 'primary',
  no_payload: 'danger',
  superseded: 'warning',
};

const RUN_BADGES: Record<string, string> = {
  ready: 'success',
  started: 'primary',
  building: 'primary',
  failed: 'danger',
  skipped: 'secondary',
};

// payloadUsage lists what the consumers did with one payload (by block
// hash): the bids that committed to it and its inclusion.
const payloadUsage = (result: SlotResult, hash: string): string[] => {
  const same = (other?: string) => !!other && other.toLowerCase() === hash.toLowerCase();
  const usage: string[] = [];
  const bids = (result.bids ?? []).filter((bid) => same(bid.block_hash));

  const p2p = bids.filter((bid) => bid.transport === 'p2p' && bid.status === 'submitted').length;
  if (p2p > 0) usage.push(p2p > 1 ? `${p2p} bids` : 'bid');

  const served = bids.filter((bid) => bid.transport !== 'p2p' && bid.status === 'served').length;
  if (served > 0) usage.push(served > 1 ? `served ${served}×` : 'served');

  if (same(result.inclusion?.block_hash)) usage.push('included');

  return usage;
};

// buildInfo is a testing run's transaction summary (the row's tooltip).
const buildInfo = (build: BuildRecord): string[] => {
  const info = build.info;
  if (!info) return [];

  const lines = [`submitted ${info.submitted_txs < 0 ? 'the EL mempool' : `${info.submitted_txs} txs`} (${info.tx_source})`];
  if (info.selection) lines.push(`selected ${info.selection.selected} of ${info.selection.pool_size} pooled`);
  if (info.fill_txs) lines.push(`${info.fill_txs} EL mempool fill txs`);
  if (info.explicit_txs) lines.push(`explicit list of ${info.explicit_txs}`);
  if (info.inclusion_list_txs) lines.push(`${info.inclusion_list_txs} inclusion-list txs`);

  return lines;
};

// buildWarnings are the testing run facts worth a line of their own.
const buildWarnings = (build: BuildRecord): string[] => {
  const info = build.info;
  const warnings: string[] = [];

  if (build.error) warnings.push(build.error);
  if (!info) return warnings;
  if (info.inclusion_list_dropped) warnings.push('inclusion list dropped');
  if (info.dropped_by_el) warnings.push(`${info.dropped_by_el} txs dropped by the EL`);
  if (info.attempts && info.attempts > 1) warnings.push(`${info.attempts} attempts`);
  if (info.dropped?.length) {
    warnings.push(
      `dropped after EL refusal: ${info.dropped.map((d) => (d.hash ? `${d.hash.slice(0, 10)}… (${d.reason})` : d.reason)).join(', ')}`
    );
  }
  if (info.fill_error) warnings.push(`EL mempool fill failed: ${info.fill_error}`);
  if (info.fill_blobs_dropped) warnings.push('fill blob txs dropped');
  if (info.fill_dropped) warnings.push('fill dropped');

  return warnings;
};

const engineTiming = (build: BuildRecord): string | undefined => {
  const calls = build.calls;
  if (!calls) return undefined;
  const span = (from: string, to: string) => formatDuration(Date.parse(to) - Date.parse(from));
  return `forkchoiceUpdated ${span(calls.forkchoice_start, calls.forkchoice_end)}, EL build wait ${span(calls.forkchoice_end, calls.get_payload_start)}, getPayload ${span(calls.get_payload_start, calls.get_payload_end)}`;
};

const BuildRow: React.FC<{
  slot: number;
  clock: SlotClock;
  build: BuildRecord;
  result: SlotResult;
  picks: SelectionRecord[];
  current?: SelectionRecord;
  onArtifactError: (message: string) => void;
}> = ({ slot, clock, build, result, picks, current, onArtifactError }) => {
  const payload = build.payload_hash ? result.payloads?.[build.payload_hash] : undefined;
  const usage = build.payload_hash ? payloadUsage(result, build.payload_hash) : [];
  const picked = picks.filter((pick) => sameBuild(pick.build, build));
  const isCurrent = sameBuild(current?.build, build);
  const warnings = buildWarnings(build);
  const details = [...buildInfo(build), engineTiming(build)].filter(Boolean).join('\n');

  return (
    <>
      <tr className={isCurrent ? 'ap-build-current' : undefined}>
        <td title={details || undefined}>
          <span className="ap-build-name">{buildLabel(build)}</span>
          {build.status !== 'ready' && (
            <span className={`ms-1 ${badgeClass(RUN_BADGES[build.status] || 'secondary')}`}>
              {build.skip_reason ? `${build.status}: ${build.skip_reason}` : build.status}
            </span>
          )}
        </td>
        <td className="font-monospace">
          {build.payload_hash ? <CopyableHash value={build.payload_hash} chars={6} headOnly /> : <span className="text-muted">—</span>}
        </td>
        <td>
          <At clock={clock} at={build.started_at} />
        </td>
        <td className="text-end" title={engineTiming(build)}>
          {build.status !== 'started' && build.status !== 'building'
            ? formatDuration(Date.parse(build.at) - Date.parse(build.started_at))
            : '…'}
        </td>
        <td className="text-end">{payload ? `${payload.num_transactions} / ${payload.num_blobs ?? 0}` : '—'}</td>
        <td className="text-end">{payload?.block_value_wei ? weiToEth(payload.block_value_wei).replace(' ETH', '') : '—'}</td>
        <td>
          <span className="d-inline-flex flex-wrap gap-1">
            {isCurrent && (
              <Tag tone="ok" title={`picked at ${picked.map((p) => formatOffsetFrom(clock, p.at)).join(', ')}`}>
                pick
              </Tag>
            )}
            {!isCurrent && picked.length > 0 && (
              <Tag tone="neutral" title={`picked at ${picked.map((p) => formatOffsetFrom(clock, p.at)).join(', ')}, replaced later`}>
                earlier
              </Tag>
            )}
            {picked.some((p) => p.fallback) && <Tag tone="warn">fallback</Tag>}
            {usage.map((use) => (
              <Tag key={use} tone={use === 'included' ? 'ok' : 'info'}>
                {use}
              </Tag>
            ))}
          </span>
        </td>
        <td className="text-end">
          {payload?.captured && build.payload_hash && (
            <ArtifactButtons
              url={payloadArtifactUrl(slot, build.payload_hash)}
              filename={`slot-${slot}-${build.kind}${build.run ? `-${build.run}` : ''}-${build.payload_hash.slice(0, 10)}.ssz`}
              onError={onArtifactError}
            />
          )}
        </td>
      </tr>
      {warnings.length > 0 && (
        <tr className="ap-build-note">
          <td colSpan={8} className={build.status === 'failed' ? 'text-danger' : 'text-warning'}>
            {warnings.join(' · ')}
          </td>
        </tr>
      )}
    </>
  );
};

const formatOffsetFrom = (clock: SlotClock, at: string): string =>
  clock.start !== undefined ? formatOffset(Date.parse(at) - clock.start) : new Date(at).toLocaleTimeString();

// PayloadFacts is a payload's header in short: the numbers in a grid, the
// hashes in a compact list.
const PayloadFacts: React.FC<{ payload: PayloadRecord; clock: SlotClock }> = ({ payload, clock }) => (
  <>
    <div className="row g-2 mb-2">
      {payload.block_number !== undefined && <KV label="Block #">{payload.block_number.toLocaleString()}</KV>}
      <KV label="Contents">
        {payload.num_transactions ?? 0} txs, {payload.num_blobs ?? 0} blobs, {payload.num_withdrawals ?? 0} wdrls,{' '}
        {payload.num_execution_requests ?? 0} reqs
      </KV>
      {payload.gas_used !== undefined && (
        <KV label="Gas used">
          {payload.gas_used.toLocaleString()}
          {payload.gas_limit ? (
            <span className="text-muted fw-normal"> ({((payload.gas_used / payload.gas_limit) * 100).toFixed(1)}% of {payload.gas_limit.toLocaleString()})</span>
          ) : null}
        </KV>
      )}
      {payload.base_fee_per_gas && (
        <KV label="Base fee">{(Number(payload.base_fee_per_gas) / 1e9).toLocaleString(undefined, { maximumFractionDigits: 4 })} gwei</KV>
      )}
      {payload.blob_gas_used !== undefined && payload.blob_gas_used > 0 && (
        <KV label="Blob gas">
          {payload.blob_gas_used.toLocaleString()}
          <span className="text-muted fw-normal"> (excess {(payload.excess_blob_gas ?? 0).toLocaleString()})</span>
        </KV>
      )}
      {payload.extra_data && (
        <KV label="Extra data">
          <span title={payload.extra_data}>{decodeExtraData(payload.extra_data)}</span>
        </KV>
      )}
      {payload.timestamp !== undefined && <KV label="Timestamp">{payload.timestamp}</KV>}
      <KV label="Built">
        <At clock={clock} at={payload.built_at} />
      </KV>
      {payload.inclusion_list && (
        <KV
          label="Inclusion lists"
          wide
          hint={
            <>
              FOCIL (Heze): the committee lists of slot {payload.inclusion_list.slot} the payload was built
              against — the gossiped lists merged with the beacon node&apos;s attributes list. Omitted
              transactions were left out on purpose (omit mode or the plan&apos;s omit_txs).
            </>
          }
        >
          {payload.inclusion_list.lists} lists ({payload.inclusion_list.timely} timely), {payload.inclusion_list.transactions} txs
          {payload.inclusion_list.omitted ? `, ${payload.inclusion_list.omitted} omitted` : ''}
          {payload.inclusion_list.mode === 'omit' && (
            <span className={`ms-1 ${badgeClass('danger')}`} title="Built without the inclusion lists">
              omitted
            </span>
          )}
          {payload.inclusion_list.bits && <span className="ms-1 font-monospace text-secondary">bits {payload.inclusion_list.bits}</span>}
        </KV>
      )}
    </div>
    <HashList
      rows={[
        ['Block hash', payload.block_hash],
        ['Parent hash', payload.parent_hash],
        ['State root', payload.state_root],
        ['Receipts root', payload.receipts_root],
        ['Prev randao', payload.prev_randao],
        ['Fee recipient', payload.fee_recipient],
      ]}
    />
  </>
);

const AttributesFacts: React.FC<{ attributes: AttributesSnapshot }> = ({ attributes }) => {
  // The beacon parent root is the parent root on every fork but the
  // parent-payload reorg; show it only when it differs.
  const beaconRoot =
    attributes.parent_beacon_block_root &&
    attributes.parent_beacon_block_root.toLowerCase() !== attributes.parent_block_root.toLowerCase()
      ? attributes.parent_beacon_block_root
      : undefined;

  return (
    <>
      <div className="row g-2 mb-2">
        <KV label="Proposer">{attributes.proposer_index}</KV>
        <KV label="Parent block #">{attributes.parent_block_number.toLocaleString()}</KV>
        <KV label="Timestamp">{attributes.timestamp}</KV>
        <KV label="Contents">
          {attributes.num_withdrawals} wdrls
          {attributes.num_inclusion_list_txs ? `, ${attributes.num_inclusion_list_txs} IL txs` : ''}
        </KV>
        {attributes.target_gas_limit !== undefined && attributes.target_gas_limit > 0 && (
          <KV label="Target gas limit">{attributes.target_gas_limit.toLocaleString()}</KV>
        )}
      </div>
      <HashList
        rows={[
          ['Parent hash', attributes.parent_block_hash],
          ['Parent root', attributes.parent_block_root],
          ['Beacon parent root', beaconRoot],
          ['Prev randao', attributes.prev_randao],
          ['Fee recipient', attributes.suggested_fee_recipient],
        ]}
      />
    </>
  );
};

const BuildTargetView: React.FC<{
  slot: number;
  clock: SlotClock;
  target: BuildTargetRecord;
  primary: boolean;
  multiple: boolean;
  result: SlotResult;
  onArtifactError: (message: string) => void;
}> = ({ slot, clock, target, primary, multiple, result, onArtifactError }) => {
  const picks = target.selections ?? [];
  const current = target.status === 'selected' ? picks[picks.length - 1] : undefined;
  const picked = current ? result.payloads?.[current.payload_hash] : undefined;
  const builds = [...(target.engine ? [target.engine] : []), ...(target.testing ?? [])];

  return (
    <div className={`ap-target${primary ? '' : ' ap-target-secondary'}`}>
      <div className="d-flex flex-wrap align-items-center gap-2 mb-1">
        <strong className="small">{target.candidate || 'unclassified'}</strong>
        {multiple && primary && <span className={badgeClass('info')}>primary</span>}
        <StatusBadge map={TARGET_BADGES} status={target.status} />
        <span className="text-muted small ms-auto">
          attributes at <At clock={clock} at={target.started_at} />
        </span>
      </div>
      {target.error && <div className="alert alert-danger small py-1 px-2 mb-2">{target.error}</div>}

      {builds.length === 0 ? (
        <div className="text-muted small mb-2">no build ran</div>
      ) : (
        <div className="table-responsive">
          <table className="table table-sm small mb-1 ap-build-table">
            <thead>
              <tr>
                <th>
                  Build
                  <InfoHint>
                    The engine build (forkchoiceUpdated → getPayload) and the testing_buildBlockV1 runs (tx
                    source <code>{target.tx_source || '—'}</code>, payload source{' '}
                    <code>{target.payload_source || '—'}</code>). Every build has its own payload; hover a
                    build for its timings and transactions. <strong>pick</strong> marks the payload the
                    consumers got last, <strong>earlier</strong> one they got before; the other tags say
                    what the consumers did with a payload.
                  </InfoHint>
                </th>
                <th>Block</th>
                <th>Start</th>
                <th className="text-end">Took</th>
                <th className="text-end" title="Transactions / blobs">
                  Txs
                </th>
                <th className="text-end">ETH</th>
                <th>Use</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {builds.map((build) => (
                <BuildRow
                  key={buildLabel(build)}
                  slot={slot}
                  clock={clock}
                  build={build}
                  result={result}
                  picks={picks}
                  current={current}
                  onArtifactError={onArtifactError}
                />
              ))}
            </tbody>
          </table>
          {!target.testing?.length && target.testing_skip_reason && (
            <div className="text-muted small mb-1">testing build skipped: {target.testing_skip_reason}</div>
          )}
        </div>
      )}

      {picked && (
        <Fold title="Picked payload" extra={`${buildLabel(picked.build)} · ${weiToEth(picked.block_value_wei)}`}>
          <PayloadFacts payload={picked} clock={clock} />
        </Fold>
      )}
      {target.attributes && (
        <Fold title="Payload attributes" extra={`proposer ${target.attributes.proposer_index}`}>
          <AttributesFacts attributes={target.attributes} />
        </Fold>
      )}
    </div>
  );
};

// ---------------------------------------------------------------------------
// Applied plan (folded): the frozen settings every module acted on
// ---------------------------------------------------------------------------

const planSummary = (frozen: FrozenPlan): string =>
  [
    frozen.build.build ? 'build' : `skip (${frozen.build.skip_reason || '—'})`,
    frozen.bid ? 'p2p bid' : 'no p2p bid',
    frozen.builder_api ? 'Builder API' : 'no Builder API',
    frozen.reveal?.suppressed ? 'reveal withheld' : 'reveal',
  ].join(' · ');

const FrozenPlanSection: React.FC<{ frozen: FrozenPlan; clock: SlotClock }> = ({ frozen, clock }) => (
  <Fold
    title={
      <>
        Applied plan
        <InfoHint>
          The immutable snapshot taken when execution for this slot began (about one slot ahead): the
          slot&apos;s plan (or matching rule) merged with the global config of that moment. Every module
          acted on exactly these values; later config or rule changes do not alter it.
        </InfoHint>
      </>
    }
    extra={
      <>
        {frozen.plan ? (frozen.plan.rule_id ? `rule ${frozen.plan.rule_id}` : 'slot plan') : 'global baseline'} ·{' '}
        {planSummary(frozen)}
      </>
    }
  >
    <div className="d-flex flex-wrap align-items-center gap-2 small text-muted mb-2">
      <span className={badgeClass('secondary')}>{frozen.fork}</span>
      frozen at <At clock={clock} at={frozen.frozen_at} />
    </div>

    <div className="section-header mb-1">Build</div>
    <div className="row g-2 mb-1">
      <KV
        label="Decision"
        hint={
          <dl>
            <dt>build</dt>
            <dd>a payload was built; <em>forced</em> = the plan pushed it past the schedule (does not consume the next_n budget)</dd>
            <dt>skip (schedule)</dt>
            <dd>the global schedule (start slot, every_nth, exhausted next_n) excluded the slot</dd>
            <dt>skip (plan_disabled)</dt>
            <dd>the plan suppressed the consumers that would otherwise have been active</dd>
            <dt>skip (no_consumer)</dt>
            <dd>no bidding or serving was active for the slot, without a plan being responsible</dd>
          </dl>
        }
      >
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
      <KV label="Start" hint="When the payload build starts, in ms relative to slot start (negative = before the slot starts).">
        {frozen.build.build_start_time_ms} ms
      </KV>
      {frozen.build.local?.enabled && (
        <KV
          label="Testing build"
          wide
          hint="testing_buildBlockV1: the transaction source it builds from and the rule that picks the payload the consumers get."
        >
          {frozen.build.local.tx_source} → {frozen.build.local.payload_source}
          {frozen.build.local.forced && <span className={`ms-1 ${badgeClass('info')}`}>forced by plan</span>}
          {frozen.build.local.tx_source === 'explicit' && (
            <span className="ms-1 fw-normal">
              {frozen.build.local.transactions_redacted ? (
                <HiddenTransactionsNote count={frozen.build.local.transactions_redacted} />
              ) : (
                <span className="text-muted small">{frozen.build.local.transactions?.length ?? 0} explicit transactions</span>
              )}
            </span>
          )}
        </KV>
      )}
    </div>

    <div className="section-header mb-1 mt-2">
      Bid (p2p)
      <InfoHint>
        Effective p2p bid settings. &quot;off for this slot&quot; means no p2p bidding: the plan disabled
        it, or ePBS was globally off (or not applicable to the fork) without a custom plan.
      </InfoHint>
    </div>
    {frozen.bid ? (
      <div className="row g-2 mb-1">
        <KV label="Window" hint="Bid start – bid end, in ms relative to slot start.">
          {frozen.bid.start_ms} – {frozen.bid.end_ms} ms
        </KV>
        <KV label="Interval" hint="Pause between successive bids of the same payload; 0 = a single bid.">
          {frozen.bid.interval_ms} ms
        </KV>
        <KV label="Min / increase" hint={<>Min: floor of <code>max(block value, min) + subsidy</code>. Increase: added per earlier bid of the same payload.</>}>
          {formatGwei(frozen.bid.min_gwei)} / {formatGwei(frozen.bid.increase_gwei)}
        </KV>
        <KV label="Subsidy" hint="Added on top of max(block value, min); negative = the margin kept from the block value. Unused when a value override is set.">
          {formatGwei(frozen.bid.subsidy_gwei)}
        </KV>
        <KV label="Value override" hint="Absolute bid base replacing max(block value, min) + subsidy; — = none, the formula applied.">
          {formatGwei(frozen.bid.value_gwei)}
        </KV>
        {(frozen.bid.forced || frozen.bid.ignore_missing_prefs) && (
          <KV
            label="Flags"
            hint={
              <dl>
                <dt>forced</dt>
                <dd>a custom plan activated bidding although ePBS is globally disabled</dd>
                <dt>ignore missing prefs</dt>
                <dd>bids went out without gossip proposer preferences, using the payload&apos;s fee recipient</dd>
              </dl>
            }
          >
            {frozen.bid.forced && <span className={`${badgeClass('warning')} me-1`}>forced</span>}
            {frozen.bid.ignore_missing_prefs && <span className={badgeClass('info')}>ignore missing prefs</span>}
          </KV>
        )}
      </div>
    ) : (
      <p className="text-muted small mb-1">off for this slot</p>
    )}

    <div className="section-header mb-1 mt-2">
      Builder API
      <InfoHint>
        Effective Builder API serving settings. &quot;off for this slot&quot; means bid requests were
        answered with 204 (no bid): disabled by the plan, or the Builder API was globally off without a
        custom plan.
      </InfoHint>
    </div>
    {frozen.builder_api ? (
      <div className="row g-2 mb-1">
        <KV label="Subsidy" hint="Added to the block value of served bids; negative = the margin kept from it (Gloas only). Unused when a total value override is set.">
          {formatGwei(frozen.builder_api.subsidy_gwei)}
        </KV>
        <KV label="Total value override" hint="Absolute total value served, replacing block value + subsidy; — = none.">
          {formatGwei(frozen.builder_api.total_value_gwei)}
        </KV>
        <KV label="Response delay" hint="How long each bid response was held before answering.">
          {frozen.builder_api.delay_ms ?? 0} ms
        </KV>
        {frozen.builder_api.forced && (
          <KV label="Flags" hint="forced: a custom plan activated serving although the Builder API is globally disabled.">
            <span className={badgeClass('warning')}>forced</span>
          </KV>
        )}
      </div>
    ) : (
      <p className="text-muted small mb-1">off for this slot</p>
    )}

    <div className="section-header mb-1 mt-2">Reveal</div>
    {frozen.reveal ? (
      <div className="row g-2 mb-1">
        <KV
          label="Status"
          hint="withheld: by a disabled plan or because reveals are globally disabled. active: a won slot gets its envelope published once the gate opens."
        >
          {frozen.reveal.suppressed ? (
            <span className={badgeClass('danger')}>withheld</span>
          ) : (
            <span className={badgeClass('success')}>active</span>
          )}
          {frozen.reveal.bypass_deadline && (
            <span
              className={`ms-1 ${badgeClass('warning')}`}
              title="A custom reveal plan may publish after the slot end, at most one extra slot."
            >
              bypass deadline
            </span>
          )}
        </KV>
        <KV
          label="Gate"
          hint={
            <dl>
              <dt>time</dt>
              <dd>reveal at the reveal time</dd>
              <dt>vote</dt>
              <dd>reveal once head votes on our block reach the threshold</dd>
              <dt>vote_or_time</dt>
              <dd>whichever opens first</dd>
              <dt>vote_and_time</dt>
              <dd>at the reveal time, once the threshold is also reached</dd>
            </dl>
          }
        >
          {frozen.reveal.gate_mode}
          <span className="text-muted fw-normal">
            {' '}
            · {frozen.reveal.reveal_time_ms} ms
            {frozen.reveal.gate_mode !== 'time' && ` · ${frozen.reveal.vote_threshold_pct}% votes`}
          </span>
        </KV>
        <KV
          label="Broadcast validation"
          hint="Validation the beacon node applied before broadcasting the envelope: gossip, consensus, or consensus_and_equivocation (adds the equivocation check that protects against unbundling)."
        >
          {frozen.reveal.broadcast_validation}
        </KV>
        <KV label="Retries" hint="Publish attempts × wait between failed attempts.">
          {frozen.reveal.max_attempts} × {frozen.reveal.retry_interval_ms} ms
        </KV>
      </div>
    ) : (
      <p className="text-muted small mb-1">—</p>
    )}

    {frozen.transforms && (frozen.transforms.payload || frozen.transforms.bid || frozen.transforms.envelope) && (
      <>
        <div className="section-header mb-1 mt-2">
          Transforms (jq)
          <InfoHint>
            jq expressions applied to this slot&apos;s objects. <strong>payload</strong> rewrote the built
            execution payload before it fed both the bid and the reveal. <strong>bid</strong> /{' '}
            <strong>envelope</strong> rewrote the message just before signing; the result was re-signed,
            so it is validly signed but customized.
          </InfoHint>
        </div>
        <div className="mb-1">
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

    {frozen.plan && (
      <>
        <div className="section-header mb-1 mt-2">
          Raw plan
          <InfoHint>
            The slot plan as frozen (a rule-derived plan carries its <code>rule_id</code>). Absent
            categories inherited the global config.
          </InfoHint>
        </div>
        <pre className="ap-raw-plan mb-0">{JSON.stringify(frozen.plan, null, 2)}</pre>
      </>
    )}
  </Fold>
);

// ---------------------------------------------------------------------------
// Bids and reveal attempts
// ---------------------------------------------------------------------------

const BidsCard: React.FC<{
  slot: number;
  clock: SlotClock;
  result: SlotResult;
  onArtifactError: (message: string) => void;
}> = ({ slot, clock, result, onArtifactError }) => {
  const bids = result.bids ?? [];
  const hasCompetitor = bids.some((bid) => bid.competitor_high_gwei !== undefined && bid.competitor_high_gwei !== null);
  const hasExecPayment = bids.some((bid) => (bid.execution_payment_gwei ?? 0) > 0);

  return (
    <div className="card mb-3">
      <div className="card-header py-1">
        <strong className="small">
          Bids ({bids.length})
          <InfoHint>
            Every bid attempt of the slot, both transports; hover a row for the bid&apos;s parent,
            randao, fee recipient, gas limit and blobs. Values in gwei. Status:
            <dl>
              <dt>submitted</dt>
              <dd>p2p bid gossiped</dd>
              <dt>served</dt>
              <dd>Builder API bid returned to the proposer</dd>
              <dt>constructed</dt>
              <dd>p2p bid built and signed, but the gossip submission failed</dd>
              <dt>suppressed</dt>
              <dd>no bid made: Builder API serving suppressed for the slot, or a p2p bid skipped before construction (e.g. missing proposer preferences)</dd>
              <dt>failed</dt>
              <dd>bid construction (or the Builder API response) failed; hover the warning icon</dd>
              <dt>cancelled</dt>
              <dd>the Builder API request ended during the planned response delay</dd>
            </dl>
            <strong>Exec payment</strong> (Builder API, Gloas): the part of the total claimed as unbacked{' '}
            <code>execution_payment</code>. <strong>Competitor</strong> (p2p): the highest other
            builder&apos;s bid known when the bid went out (our own keys excluded).
          </InfoHint>
        </strong>
        {result.dropped_attempts?.['bids'] ? (
          <span className="text-muted small ms-2">(+{result.dropped_attempts['bids']} dropped)</span>
        ) : null}
      </div>
      <div className="table-responsive">
        <table className="table table-sm table-hover small mb-0 ap-compact-table">
          <thead>
            <tr>
              <th>At</th>
              <th>Status</th>
              <th>Via</th>
              <th className="text-end">Value</th>
              {hasExecPayment && <th className="text-end">Exec payment</th>}
              {hasCompetitor && <th className="text-end">Competitor</th>}
              <th>Block</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {bids.map((bid, i) => (
              <tr
                key={i}
                title={[
                  bid.parent_block_hash ? `parent hash: ${bid.parent_block_hash}` : '',
                  bid.parent_block_root ? `parent root: ${bid.parent_block_root}` : '',
                  bid.prev_randao ? `prev randao: ${bid.prev_randao}` : '',
                  bid.fee_recipient ? `fee recipient: ${bid.fee_recipient}` : '',
                  bid.gas_limit ? `gas limit: ${bid.gas_limit.toLocaleString()}` : '',
                  `blobs: ${bid.num_blob_commitments ?? 0}`,
                  bid.builder_index !== undefined ? `builder index: ${bid.builder_index}` : '',
                  bid.key_index !== undefined ? `key: #${bid.key_index}` : '',
                ]
                  .filter(Boolean)
                  .join('\n')}
              >
                <td>
                  <At clock={clock} at={bid.at} />
                </td>
                <td>
                  <StatusBadge map={BID_BADGES} status={bid.status} />
                  {bid.error && <i className="fas fa-triangle-exclamation text-danger ms-1" title={bid.error}></i>}
                </td>
                <td>{bid.transport === 'p2p' ? 'p2p' : 'API'}</td>
                <td className="text-end font-monospace">{bid.total_value_gwei.toLocaleString()}</td>
                {hasExecPayment && (
                  <td className="text-end font-monospace">{(bid.execution_payment_gwei ?? 0).toLocaleString()}</td>
                )}
                {hasCompetitor && (
                  <td className="text-end font-monospace">
                    {bid.competitor_high_gwei !== undefined && bid.competitor_high_gwei !== null
                      ? bid.competitor_high_gwei.toLocaleString()
                      : '—'}
                  </td>
                )}
                <td className="font-monospace">
                  {bid.block_hash ? <CopyableHash value={bid.block_hash} chars={6} headOnly /> : '—'}
                </td>
                <td className="text-end">
                  {bid.artifact_index !== undefined && (
                    <ArtifactButtons
                      url={apiPath(`/slot-results/${slot}/bids/${bid.artifact_index}`)}
                      filename={`slot-${slot}-bid-${bid.artifact_index}.ssz`}
                      onError={onArtifactError}
                    />
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
};

const RevealAttemptsCard: React.FC<{ clock: SlotClock; result: SlotResult }> = ({ clock, result }) => (
  <div className="card mb-3">
    <div className="card-header py-1">
      <strong className="small">
        Reveal attempts
        <InfoHint>
          <dl>
            <dt>published</dt>
            <dd>the envelope was accepted by the beacon node</dd>
            <dt>failed</dt>
            <dd>the publish attempt failed (retried up to the attempt limit)</dd>
            <dt>suppressed</dt>
            <dd>withheld by the slot&apos;s plan (<code>plan_disabled</code>)</dd>
            <dt>skipped</dt>
            <dd>
              <code>disabled</code> (reveals globally off), <code>late</code> (the reveal request arrived
              after the slot ended) or <code>vote_gate_timeout</code> (the vote gate never opened)
            </dd>
          </dl>
          The envelope artifact is kept even when the reveal was withheld or failed.
        </InfoHint>
      </strong>
    </div>
    <div className="table-responsive">
      <table className="table table-sm small mb-0 ap-compact-table">
        <thead>
          <tr>
            <th>#</th>
            <th>Status</th>
            <th>Via</th>
            <th>Started</th>
            <th>At</th>
            <th>Detail</th>
          </tr>
        </thead>
        <tbody>
          {(result.reveal_attempts ?? []).map((attempt, i) => (
            <tr key={i}>
              <td>{attempt.attempt}</td>
              <td>
                <StatusBadge map={REVEAL_BADGES} status={attempt.status} />
              </td>
              <td>{attempt.transport}</td>
              <td>
                <At clock={clock} at={attempt.started_at} />
              </td>
              <td>
                <At clock={clock} at={attempt.at} />
              </td>
              <td className="ap-wrap">
                {attempt.skip_reason && <span className="text-muted">{attempt.skip_reason}</span>}
                {attempt.error && <span className="text-danger"> {attempt.error}</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  </div>
);

// ---------------------------------------------------------------------------
// Result view
// ---------------------------------------------------------------------------

const ResultView: React.FC<{
  slot: number;
  result?: SlotResult;
  storedPlan?: SlotPlan;
  onArtifactError: (message: string) => void;
}> = ({ slot, result, storedPlan, onArtifactError }) => {
  const clock = useSlotClock(slot);

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

  const build = result.build;
  const primary = primaryTargetRecord(build);
  const reveals = result.reveal_attempts ?? [];
  const envelopeExists = reveals.some((r) => r.status === 'published' || r.status === 'failed');
  const targets = [...(build?.targets ?? [])].sort((a, b) => (a === primary ? -1 : b === primary ? 1 : 0));

  return (
    <>
      <OutcomeCard
        rows={outcomeRows(result, clock, primary)}
        envelope={
          envelopeExists ? (
            <span className="d-inline-flex align-items-center gap-1 small text-muted">
              envelope
              <ArtifactButtons
                url={apiPath(`/slot-results/${slot}/envelope`)}
                filename={`slot-${slot}-envelope.ssz`}
                onError={onArtifactError}
              />
            </span>
          ) : undefined
        }
      />

      {build && (
        <div className="card mb-3">
          <div className="card-header py-1 d-flex flex-wrap align-items-center gap-2">
            <strong className="small">
              Builds
              <InfoHint>
                <dl>
                  <dt>waiting_attributes</dt>
                  <dd>the slot is active, payload attributes have not arrived yet</dd>
                  <dt>no_attributes</dt>
                  <dd>no payload attributes arrived for the slot, so nothing could be built</dd>
                  <dt>building</dt>
                  <dd>the builds run, no payload picked yet</dd>
                  <dt>ready</dt>
                  <dd>a payload is picked for the consumers</dd>
                  <dt>failed</dt>
                  <dd>no build produced a payload to pick (see the error)</dd>
                  <dt>skipped</dt>
                  <dd>the build decision skipped the slot (schedule, plan_disabled, no_consumer)</dd>
                </dl>
                One table per build target (a parent the slot built on): its engine build and testing
                runs, each with its own payload.
              </InfoHint>
            </strong>
            <StatusBadge map={BUILD_BADGES} status={build.status} />
            {build.skip_reason && <span className="text-muted small">({build.skip_reason})</span>}
          </div>
          <div className="card-body py-2">
            {build.error && <div className="alert alert-danger small py-1 px-2 mb-2">{build.error}</div>}
            {targets.length === 0 && <div className="text-muted small">No build target ran.</div>}
            {targets.map((target, i) => (
              <BuildTargetView
                key={`${target.candidate ?? ''}-${i}`}
                slot={slot}
                clock={clock}
                target={target}
                primary={target === primary}
                multiple={targets.length > 1}
                result={result}
                onArtifactError={onArtifactError}
              />
            ))}
          </div>
        </div>
      )}

      {(result.bids?.length ?? 0) > 0 && (
        <BidsCard slot={slot} clock={clock} result={result} onArtifactError={onArtifactError} />
      )}

      {reveals.length > 1 && <RevealAttemptsCard clock={clock} result={result} />}

      {result.applied_plan && (
        <div className="card mb-3">
          <div className="card-body py-2">
            <FrozenPlanSection frozen={result.applied_plan} clock={clock} />
          </div>
        </div>
      )}
    </>
  );
};

// ---------------------------------------------------------------------------
// Modal
// ---------------------------------------------------------------------------

interface SlotEditDrawerProps {
  target: ModalTarget;
  plans: Record<number, SlotPlan>;
  results: Record<number, SlotResult>;
  currentSlot: number;
  canEdit: boolean;
  applyUpdates: (updates: PlanUpdate[]) => Promise<ApplyUpdatesResult>;
  onClose: () => void;
  // Reports unsaved edits, so the page can ask before a selection change
  // replaces the drawer's target.
  onDirtyChange?: (dirty: boolean) => void;
}

// The plan editor's sections, grouped like the dashboard's flow cards.
type PlanTab = 'result' | 'engine' | 'testing' | 'bid' | 'builder_api' | 'reveal' | 'advanced';

// SlotEditDrawer shows a past slot's outcome or edits the plan of the selected
// future slots. It is a side drawer, not a modal: the grid stays usable, so the
// selection can still change while it is open.
export const SlotEditDrawer: React.FC<SlotEditDrawerProps> = ({
  target,
  plans,
  results,
  currentSlot,
  canEdit,
  applyUpdates,
  onClose,
  onDirtyChange,
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

  // Local build (testing_buildBlockV1) overrides. Bulk starts empty and only
  // writes when something is set; single mode edits the slot's own overrides.
  const [localBuild, setLocalBuild] = useState<LocalBuildFormState>(() =>
    initLocalBuildState(bulk ? undefined : effectivePlan?.build?.local)
  );
  const { status: localBuildStatus } = useLocalBuildStatus();

  // Inclusion list (FOCIL) overrides: bulk starts empty and only writes what
  // is set, single mode edits the slot's own overrides.
  const [inclusionList, setInclusionList] = useState<InclusionListFormState>(() =>
    initInclusionListState(bulk ? undefined : effectivePlan?.build?.inclusion_list)
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

  const [activeTab, setActiveTab] = useState<PlanTab>(isPastSingle ? 'result' : 'bid');

  // Unsaved edits: the form state differs from the one it opened with.
  const formSnapshot = JSON.stringify([
    bidState, apiState, revealState, buildReorg, localBuild, buildCandidates, inclusionList, transforms, ignoreRules,
  ]);
  const initialSnapshot = useMemo(() => formSnapshot, []); // eslint-disable-line react-hooks/exhaustive-deps
  const dirty = !isPastSingle && formSnapshot !== initialSnapshot;
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

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
      onDirtyChange?.(false);
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
    const setPaths: Record<string, number | string | boolean | object | null> = {};

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

    const localPlan = localBuildPlanFromState(localBuild);
    const initialLocal = initialPlan?.build?.local;
    const localChanged = bulk
      ? localPlan !== undefined
      : JSON.stringify(localPlan ?? null) !== JSON.stringify(initialLocal ?? null);

    const ilPlan = inclusionListPlanFromState(inclusionList);
    const ilChanged = bulk
      ? ilPlan !== undefined
      : JSON.stringify(ilPlan ?? null) !== JSON.stringify(initialPlan?.build?.inclusion_list ?? null);
    const ilError = ilChanged ? inclusionListError(inclusionList) : null;
    if (ilError) {
      setFormError(ilError);
      return;
    }

    // A hidden explicit list cannot be written back: the backend refuses it
    // rather than dropping the transactions.
    const hiddenError = (candidatesChanged || localChanged) ? hiddenTransactionsError(localPlan) : null;
    if (hiddenError) {
      setFormError(hiddenError);
      return;
    }

    if (candidatesChanged) {
      const buildObj: Record<string, unknown> = {};
      const wantReorg = buildReorg === 'on';

      if (wantReorg) buildObj.reorg_parent_payload = true;

      if (candidateEntries.length > 0) {
        buildObj.candidates = Object.fromEntries(candidateEntries);
      }

      if (localPlan) buildObj.local = localPlan;
      // The category is replaced whole: the inclusion list overrides go along.
      if (ilPlan) buildObj.inclusion_list = ilPlan;

      updateRec['build'] = Object.keys(buildObj).length > 0 ? buildObj : null;
      hasChange = true;
    } else if (localChanged) {
      // The local overrides are a sub-object of the build category, written
      // as one fine-grained set path (null clears them).
      setPaths['build.local'] = localPlan ?? null;
      hasChange = true;

      if (buildReorg !== 'unchanged') {
        const initialReorg = initialPlan?.build?.reorg_parent_payload === true;
        const wantOn = buildReorg === 'on';
        if (!isSingle || wantOn !== initialReorg) {
          setPaths['build.reorg_parent_payload'] = wantOn;
        }
      }
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

    // Inclusion list overrides as one set path (null clears them), unless the
    // whole build category was replaced above.
    if (!candidatesChanged && ilChanged) {
      setPaths['build.inclusion_list'] = ilPlan ?? null;
      hasChange = true;
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

  const customized = (mode: string) => mode === 'custom' || mode === 'disabled';
  const marked = (on: boolean) =>
    on ? <span className="sd-dot" aria-label="customized for this slot" /> : undefined;
  const candidatesSet = Object.values(buildCandidates).some((mode) => mode !== '');
  const localSet = JSON.stringify(localBuild) !== JSON.stringify(EMPTY_LOCAL_BUILD_STATE);

  const tabs = isPastSingle
    ? [{ id: 'result' as PlanTab, label: 'Result' }]
    : [
        {
          id: 'engine' as PlanTab,
          label: 'Engine builder',
          extra: marked(buildReorg === 'on' || candidatesSet || inclusionListPlanFromState(inclusionList) !== undefined),
        },
        { id: 'testing' as PlanTab, label: 'Testing builder', extra: marked(localSet) },
        { id: 'bid' as PlanTab, label: 'ePBS bidder', extra: marked(customized(bidState.mode)) },
        { id: 'builder_api' as PlanTab, label: 'Builder API', extra: marked(customized(apiState.mode)) },
        { id: 'reveal' as PlanTab, label: 'Payload reveal', extra: marked(customized(revealState.mode)) },
        {
          id: 'advanced' as PlanTab,
          label: 'Advanced',
          extra: marked(ignoreRules || Object.values(transforms).some((v) => v.trim() !== '')),
        },
      ];
  const tab = tabs.some((t) => t.id === activeTab) ? activeTab : tabs[0].id;

  const slotState = isPastSingle
    ? { label: 'past', tone: 'neutral' as const }
    : bulk
      ? { label: `${count} slots`, tone: 'accent' as const }
      : rulePlan
        ? { label: `rule ${rulePlan.rule_id}`, tone: 'info' as const }
        : initialPlan
          ? { label: 'planned', tone: 'ok' as const }
          : { label: 'global baseline', tone: 'neutral' as const };

  return (
    <aside className="sd-panel-root ap-plan-drawer" role="dialog" aria-labelledby="ap-drawer-title">
      <div className="sd-head">
        <div className="sd-title-row">
          <h2 id="ap-drawer-title" className="sd-title">
            {isPastSingle ? targetLabel : `Plan — ${targetLabel}`}
          </h2>
          <Tag tone={slotState.tone}>{slotState.label}</Tag>
          {dirty && <Tag tone="warn">unsaved</Tag>}
          <div className="sd-head-actions">
            <button type="button" className="bd-btn bd-btn-sm" aria-label="Close" onClick={onClose}>
              <i className="fas fa-xmark" aria-hidden="true"></i>
            </button>
          </div>
        </div>
        {!isPastSingle && (
          <p className="sd-description">
            {bulk
              ? `Changes apply to all ${count} selected slots. "unchanged" leaves a category as it is per slot; "inherit (clear)" removes it.`
              : 'Per-slot instructions on top of the global settings. Shift-click extends the selection, Ctrl/Cmd-click toggles slots.'}
          </p>
        )}
        <Tabs label="Plan sections" active={tab} onChange={setActiveTab} tabs={tabs} />
      </div>

      <div className="sd-body">
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
            {rulePlan && (
              <div className="alert alert-secondary small py-2">
                <i className="fas fa-repeat me-1"></i>
                This slot has no plan of its own — it follows recurring rule{' '}
                <strong>{rulePlan.rule_id}</strong>, whose values are pre-filled. Saving stores them
                as an explicit plan for this slot, detaching it from the rule.
              </div>
            )}

            {tab === 'engine' && (
              <>
                <BuildForm bulk={bulk} value={buildReorg} disabled={formDisabled} onChange={setBuildReorg} />
                {bulk ? (
                  <div className="form-text">Build candidates can only be set per slot.</div>
                ) : (
                  <CandidatesForm state={buildCandidates} disabled={formDisabled} onChange={setBuildCandidates} />
                )}
                <InclusionListForm
                  bulk={bulk}
                  state={inclusionList}
                  disabled={formDisabled}
                  onChange={setInclusionList}
                />
              </>
            )}

            {tab === 'testing' && (
              <LocalBuildForm
                bulk={bulk}
                state={localBuild}
                disabled={formDisabled}
                available={localBuildStatus?.availability.available ?? false}
                unavailableReason={localBuildStatus?.availability.reason}
                onChange={setLocalBuild}
              />
            )}

            {tab === 'bid' && (
              <CategoryForm
                title="Bid (p2p)"
                hint={BID_CATEGORY_HINT}
                bulk={bulk}
                state={bidState}
                fields={BID_FIELDS}
                disabled={formDisabled}
                showIgnorePrefs
                onChange={setBidState}
              />
            )}

            {tab === 'builder_api' && (
              <CategoryForm
                title="Builder API"
                hint={BUILDER_API_CATEGORY_HINT}
                bulk={bulk}
                state={apiState}
                fields={BUILDER_API_FIELDS}
                disabled={formDisabled}
                onChange={setApiState}
              />
            )}

            {tab === 'reveal' && (
              <CategoryForm
                title="Reveal"
                hint={REVEAL_CATEGORY_HINT}
                bulk={bulk}
                state={revealState}
                fields={REVEAL_FIELDS}
                disabled={formDisabled}
                onChange={setRevealState}
              />
            )}

            {tab === 'advanced' && (
              <>
                <div className="mb-3">
                  <div className="section-header mb-1">
                    Recurring rules
                    <InfoHint>
                      Rules never merge with a plan: any explicit plan for the slot replaces a
                      matching rule entirely. Ignoring rules opts the slot out without setting any
                      category, so it runs on the plain global baseline.
                      {bulk && ' Checked applies the opt-out to every selected slot; unchecked leaves each slot as it is.'}
                    </InfoHint>
                  </div>
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
                    </label>
                  </div>
                </div>
                <TransformEditor
                  bulk={bulk}
                  value={transforms}
                  disabled={formDisabled}
                  sampleSlot={isSingle ? singleSlot : undefined}
                  onChange={setTransforms}
                />
              </>
            )}
          </form>
        )}
      </div>

      <div className="sd-foot">
        {showEditForm && canEdit && deletable && (
          <button type="button" className="bd-btn bd-btn-sm bd-btn-danger" disabled={saving} onClick={handleDelete}>
            <i className="fas fa-trash"></i>
            {bulk ? 'Delete plans' : 'Delete plan'}
          </button>
        )}
        <span className="sd-foot-text"></span>
        <button type="button" className="bd-btn bd-btn-sm" onClick={onClose}>
          {showEditForm ? 'Cancel' : 'Close'}
        </button>
        {showEditForm && canEdit && (
          <button type="submit" form="ap-edit-form" className="bd-btn bd-btn-sm bd-btn-primary" disabled={saving || !dirty}>
            {saving && <span className="spinner-border spinner-border-sm"></span>}
            Save
          </button>
        )}
      </div>
    </aside>
  );
};
