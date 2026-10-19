import type {
  BuildRef,
  EngineCalls,
  InclusionListBuild,
  LocalBuildInfo,
  PayloadInfo,
  SlotState,
  TargetState,
} from '../types';
import { buildLabel, payloadArtifactUrl, payloadByHash, sameBuild } from '../buildTargets';
import { decodeExtraData, formatGwei } from '../utils';
import type { PopoverData, PopoverItem } from './Popover';

// The slot graph's build lanes are drawn from separate entities: the engine
// lane from a target's engine build, the testing lane from its testing runs,
// each with its own payload (by block hash). The pick — the payload the cache
// hands to the consumers — only marks the picked build's dot, and what the
// consumers used is listed per payload (matched by block hash). No lane
// looks at another to decide what to draw, and no build shows another's
// payload.

const gwei = (wei: string | undefined) => formatGwei(Number(BigInt(wei || '0') / 1000000000n));

const truncate = (hash: string, len = 16) => (hash.length <= len + 3 ? hash : `${hash.substring(0, len)}...`);

// inclusionListItems describes the inclusion list view (FOCIL, Heze) a
// payload was built against; placed/skipped are the testing build's
// placement (absent for the engine build, which hands the list to the EL).
export function inclusionListItems(
  il: InclusionListBuild | undefined,
  placed?: number,
  skipped?: Record<string, number>,
): PopoverItem[] {
  if (!il) return [];

  const items: PopoverItem[] = [
    {
      label: 'Inclusion Lists',
      value: `${il.lists} lists (${il.timely} timely)${il.equivocators ? `, ${il.equivocators} equivocators ignored` : ''}`,
    },
    {
      label: 'List Txs',
      value:
        `${il.transactions}` +
        (il.omitted ? `, ${il.omitted} omitted (${il.mode === 'omit' ? 'omit mode' : 'omit_txs'})` : ''),
    },
  ];

  if (placed !== undefined) {
    const skips = Object.entries(skipped ?? {}).map(([reason, n]) => `${reason}: ${n}`);
    items.push({
      label: 'List Placement',
      value: `${placed} placed${skips.length > 0 ? `, could not execute: ${skips.join(', ')}` : ''}`,
    });
  }

  if (il.bits) items.push({ label: 'Committee Bits', value: il.bits });

  return items;
}

// parentItems renders the parent tuple a payload was built on.
function parentItems(payload: PayloadInfo): PopoverItem[] {
  return [
    ...(payload.parent_block_root
      ? [{
          label: payload.parent_slot !== undefined ? `Parent Block (slot ${payload.parent_slot})` : 'Parent Block',
          value: truncate(payload.parent_block_root),
          copyValue: payload.parent_block_root,
        }]
      : []),
    {
      label: payload.parent_block_number ? `Parent Payload (#${payload.parent_block_number})` : 'Parent Payload',
      value: truncate(payload.parent_block_hash),
      copyValue: payload.parent_block_hash,
    },
  ];
}

// payloadItems renders one payload's properties.
export function payloadItems(payload: PayloadInfo): PopoverItem[] {
  return [
    { label: 'Block Hash', value: truncate(payload.block_hash), copyValue: payload.block_hash },
    { label: 'Block Value', value: gwei(payload.block_value) },
    ...parentItems(payload),
    ...(payload.block_number !== undefined ? [{ label: 'Block #', value: `${payload.block_number}` }] : []),
    ...(payload.fee_recipient
      ? [{ label: 'Fee Recipient', value: truncate(payload.fee_recipient), copyValue: payload.fee_recipient }]
      : []),
    ...(payload.gas_used !== undefined
      ? [{ label: 'Gas', value: `${payload.gas_used.toLocaleString()} / ${(payload.gas_limit ?? 0).toLocaleString()}` }]
      : []),
    ...(payload.base_fee_per_gas ? [{ label: 'Base Fee', value: `${payload.base_fee_per_gas} wei` }] : []),
    ...(payload.extra_data
      ? [{ label: 'Extra Data', value: decodeExtraData(payload.extra_data), copyValue: payload.extra_data }]
      : []),
    ...(payload.blob_gas_used
      ? [{
          label: 'Blob Gas',
          value: `${payload.blob_gas_used.toLocaleString()} (excess ${(payload.excess_blob_gas ?? 0).toLocaleString()})`,
        }]
      : []),
    {
      label: 'Contents',
      value: `${payload.num_transactions} txs, ${payload.num_blobs} blobs, ` +
        `${payload.num_withdrawals} wdrls, ${payload.num_exec_requests} reqs`,
    },
  ];
}

// pickItems tells when the build was the target's pick; a build that rebuilt
// an already picked block exactly is not picked itself.
function pickItems(target: TargetState, build: BuildRef, payloadHash: string | undefined, slotStartTime: number): PopoverItem[] {
  if (!payloadHash) return [];

  const index = target.selections.findIndex((s) => sameBuild(s.build, build));
  if (index < 0) {
    const same = target.selections.find((s) => s.payloadHash === payloadHash);
    return [{ label: 'Pick', value: same ? `not picked (same block as ${buildLabel(same.build)})` : 'never picked' }];
  }

  const selection = target.selections[index];
  const replaced = target.selections[index + 1];

  return [{
    label: 'Pick',
    value: `${selection.reason === 'first' ? 'first pick' : 'better pick'} at ${selection.at - slotStartTime}ms` +
      `${selection.fallback ? ' (fallback)' : ''}` +
      (replaced ? `, until ${buildLabel(replaced.build)} at ${replaced.at - slotStartTime}ms` : ', current pick'),
  }];
}

// usageItems lists what the consumers did with a payload: the p2p bids and
// Builder API responses that committed to its block hash, its reveal and its
// inclusion.
export function usageItems(state: SlotState, payloadHash: string | undefined, slotStartTime: number): PopoverItem[] {
  if (!payloadHash) return [];

  const same = (other?: string) => !!other && other.toLowerCase() === payloadHash.toLowerCase();
  const used: string[] = [];

  const bids = (state.ourBids ?? []).filter((bid) => bid.success && same(bid.blockHash));
  if (bids.length > 0) {
    used.push(`${bids.length} p2p bid${bids.length > 1 ? 's' : ''} at ${bids.map((bid) => `${bid.time - slotStartTime}ms`).join(', ')}`);
  }

  if (same(state.getHeaderBlockHash) && state.getHeaderDeliveredAt) {
    used.push(`getHeader served at ${state.getHeaderDeliveredAt - slotStartTime}ms`);
  }

  if (same(state.getBidBlockHash) && state.getBidDeliveredAt) {
    used.push(`bid served at ${state.getBidDeliveredAt - slotStartTime}ms`);
  }

  const reveal = state.revealAttempts?.find((attempt) => attempt.success && same(attempt.envelope?.block_hash));
  if (reveal) used.push(`revealed at ${reveal.time - slotStartTime}ms`);

  if (state.bidWon && (same(state.blockDetail?.bid?.block_hash) || same(state.blockDetail?.payload?.block_hash))) {
    used.push('included');
  }

  return [{ label: 'Used By', value: used.length > 0 ? used.join(', ') : 'nothing' }];
}

// selectionState reports whether the build is the target's current pick.
function selectionState(target: TargetState, build: BuildRef) {
  const last = target.selections[target.selections.length - 1];

  return { current: target.status === 'selected' && sameBuild(last?.build, build) };
}

export interface BuildView {
  key: string;
  build: BuildRef;
  status: 'started' | 'building' | 'ready' | 'failed' | 'skipped';
  startedAt: number; // unix ms
  endAt?: number; // unix ms; absent while running
  calls?: EngineCalls;
  // The build's payload is the target's pick now.
  selected: boolean;
  data: PopoverData;
}

// engineBuildData is the popover content of one target's engine build: the
// build's calls and its own payload.
function engineBuildData(
  state: SlotState,
  target: TargetState,
  slot: number,
  slotStartTime: number,
): { items: PopoverItem[]; artifact?: PopoverData['artifact'] } {
  const engine = target.engineBuild;
  if (!engine) return { items: [{ label: 'Status', value: 'not started' }] };

  const payload = payloadByHash(state, engine.payloadHash);
  const calls = engine.calls;

  const items: PopoverItem[] = [
    { label: 'Build Start', value: `${engine.startedAt - slotStartTime}ms` },
    ...(calls
      ? [
          { label: 'forkchoiceUpdated', value: `${calls.fcu_start - slotStartTime}ms, took ${calls.fcu_end - calls.fcu_start}ms` },
          { label: 'EL building', value: `${calls.get_payload_start - calls.fcu_end}ms (waiting for getPayload)` },
          { label: 'getPayload', value: `${calls.get_payload_start - slotStartTime}ms, took ${calls.get_payload_end - calls.get_payload_start}ms` },
        ]
      : []),
    ...(engine.status === 'started'
      ? [{ label: 'Status', value: 'building…' }]
      : [
          { label: engine.status === 'ready' ? 'Built At' : 'Failed At', value: `${engine.at - slotStartTime}ms` },
          { label: 'Duration', value: `${engine.at - engine.startedAt}ms` },
        ]),
    ...(engine.error ? [{ label: 'Error', value: engine.error }] : []),
    ...pickItems(target, { kind: 'engine' }, engine.payloadHash, slotStartTime),
    ...usageItems(state, engine.payloadHash, slotStartTime),
    ...(payload ? [...payloadItems(payload), ...inclusionListItems(payload.inclusion_list)] : []),
  ];

  return {
    items,
    artifact: payload
      ? { url: payloadArtifactUrl(slot, payload.block_hash), filename: `slot-${slot}-engine-${payload.block_hash.slice(0, 10)}.ssz` }
      : undefined,
  };
}

const CANDIDATE_SHORT: Record<string, string> = {
  parent_full: 'parent',
  parent_empty: 'parent (empty)',
  grandparent_full: 'grandparent',
  grandparent_empty: 'grandparent (empty)',
};

export const candidateShortLabel = (candidate?: string): string =>
  (candidate && CANDIDATE_SHORT[candidate]) || 'unclassified';

// engineBuildView describes the engine lane of the primary target; the other
// targets' engine builds are tabs of its popover (each with its own payload).
export function engineBuildView(
  state: SlotState,
  primary: TargetState | undefined,
  slot: number,
  slotStartTime: number,
  candidateColor: (candidate?: string) => string | undefined,
  candidateOrder: (candidate?: string) => number,
): BuildView | undefined {
  const engine = primary?.engineBuild;
  if (!primary || !engine) return undefined;

  const { current } = selectionState(primary, { kind: 'engine' });
  const own = engineBuildData(state, primary, slot, slotStartTime);
  const others = Object.values(state.targets ?? {}).filter((t) => t.engineBuild);

  return {
    key: `engine-${primary.seq}`,
    build: { kind: 'engine' },
    status: engine.status,
    startedAt: engine.startedAt,
    endAt: engine.status === 'started' ? undefined : engine.at,
    calls: engine.calls,
    selected: current,
    data: {
      title: 'Engine Build',
      wide: true,
      items: own.items,
      artifact: own.artifact,
      tabs: others.length > 1
        ? others
            .sort((a, b) => candidateOrder(a.candidate) - candidateOrder(b.candidate))
            .map((target) => {
              const data = engineBuildData(state, target, slot, slotStartTime);
              return {
                key: `${target.candidate || 'unclassified'}-${target.seq}`,
                label: candidateShortLabel(target.candidate),
                color: candidateColor(target.candidate),
                items: data.items,
                artifact: data.artifact,
              };
            })
        : undefined,
    },
  };
}

// testingInfoItems describes a testing run's transaction assembly.
function testingInfoItems(info: LocalBuildInfo | undefined): PopoverItem[] {
  if (!info) return [];

  const selection = info.selection;

  return [
    ...(selection
      ? [
          { label: 'Pool Selection', value: `${selection.selected} of ${selection.pool_size} pooled (gas ${selection.gas_sum.toLocaleString()})` },
          ...(selection.fill_candidates
            ? [{
                label: 'EL Mempool Fill',
                value: `${selection.fill_selected ?? 0} of ${selection.fill_candidates} pending (gas ${(selection.fill_gas_sum ?? 0).toLocaleString()}` +
                  `${selection.fill_blobs ? `, ${selection.fill_blobs} blobs` : ''})` +
                  `${info.fill_dropped ? ', dropped after an EL refusal' : info.fill_blobs_dropped ? ', blob transactions dropped after an EL refusal' : ''}`,
              }]
            : []),
          ...(selection.skipped && Object.keys(selection.skipped).length > 0
            ? [{ label: 'Skipped', value: Object.entries(selection.skipped).map(([k, v]) => `${k}: ${v}`).join(', ') }]
            : []),
        ]
      : []),
    ...(info.explicit_txs ? [{ label: 'Plan Txs', value: `${info.explicit_txs}` }] : []),
    ...(info.fill_error ? [{ label: 'EL Mempool', value: `unreadable: ${info.fill_error}` }] : []),
    ...(info.attempts && info.attempts > 1 ? [{ label: 'Attempts', value: `${info.attempts}` }] : []),
    ...(info.dropped?.length ? [{ label: 'Dropped', value: `${info.dropped.length} after EL refusals` }] : []),
    ...(info.dropped_by_el ? [{ label: 'Dropped by EL', value: `${info.dropped_by_el}` }] : []),
    ...(info.inclusion_list_dropped ? [{ label: 'Inclusion List', value: 'dropped on retry' }] : []),
    ...inclusionListItems(info.inclusion_list, selection?.inclusion_list_txs ?? info.inclusion_list_txs,
      selection?.inclusion_list_skipped),
  ];
}

// testingRunViews describes every testing run of the target, each with its
// own payload, in run order.
export function testingRunViews(
  state: SlotState,
  target: TargetState | undefined,
  slot: number,
  slotStartTime: number,
): BuildView[] {
  if (!target) return [];

  return Object.values(target.testingRuns)
    .sort((a, b) => a.run - b.run)
    .map((run) => {
      const payload = payloadByHash(state, run.payloadHash);
      const { current } = selectionState(target, { kind: 'testing', run: run.run });
      const selectedAt = run.info?.selected_at ? Date.parse(run.info.selected_at) : undefined;

      const items: PopoverItem[] = [
        { label: 'Started', value: `${run.startedAt - slotStartTime}ms` },
        ...(selectedAt ? [{ label: 'Tx Selection', value: `${selectedAt - run.startedAt}ms` }] : []),
        ...(run.status === 'building'
          ? [{ label: 'Status', value: 'building…' }]
          : [
              {
                label: run.status === 'ready' ? 'Built At' : run.status === 'failed' ? 'Failed At' : 'Skipped At',
                value: `${run.at - slotStartTime}ms`,
              },
              { label: 'Duration', value: `${run.at - run.startedAt}ms` },
              { label: 'Status', value: `${run.status}${run.skipReason ? ` (${run.skipReason})` : ''}` },
            ]),
        ...(run.error ? [{ label: 'Error', value: run.error }] : []),
        { label: 'Tx Source', value: run.txSource || target.txSource || '—' },
        ...pickItems(target, { kind: 'testing', run: run.run }, run.payloadHash, slotStartTime),
        ...usageItems(state, run.payloadHash, slotStartTime),
        ...(payload ? payloadItems(payload) : []),
        ...testingInfoItems(run.info),
      ];

      return {
        key: `testing-${target.seq}-${run.run}`,
        build: { kind: 'testing' as const, run: run.run },
        status: run.status,
        startedAt: run.startedAt,
        endAt: run.status === 'building' ? undefined : run.at,
        selected: current,
        data: {
          title: `Testing Build #${run.run} (testing_buildBlockV1)`,
          wide: true,
          items,
          artifact: payload
            ? { url: payloadArtifactUrl(slot, payload.block_hash), filename: `slot-${slot}-testing-${run.run}-${payload.block_hash.slice(0, 10)}.ssz` }
            : undefined,
        },
      };
    });
}

// payloadOwnerItems names the build behind a payload a bid, header or block
// committed to: "engine · parent" / "testing #3 · parent".
export function payloadOwnerItems(state: SlotState, blockHash: string | undefined): PopoverItem[] {
  const payload = payloadByHash(state, blockHash);
  if (!payload) return [];

  return [{ label: 'Payload', value: `${buildLabel(payload.build)} · ${candidateShortLabel(payload.candidate)}` }];
}
