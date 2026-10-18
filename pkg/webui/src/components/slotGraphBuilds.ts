import type { LocalBuildProgressEvent, LocalBuildRun, LocalBuildStreamEvent, SlotState } from '../types';
import { formatGwei } from '../utils';
import type { PopoverData, PopoverItem } from './Popover';

// The slot graph's build lanes are independent: the engine lane is drawn from
// the engine build's own events, the testing lane from the testing builds'.
// Neither lane looks at the other to decide what to draw.

const gwei = (wei: string | undefined) => formatGwei(Number(BigInt(wei || '0') / 1000000000n));

const truncate = (hash: string, len = 16) => (hash.length <= len + 3 ? hash : `${hash.substring(0, len)}...`);

// engineBuildRan reports whether the slot's primary target ran the engine-API
// build: its payload carries the engine calls, or its build-started event says
// so. A payload without engine calls and without a start event came from the
// testing builds alone.
export function engineBuildRan(state: SlotState): boolean {
  if (state.payloadEngineCalls) return true;
  const primary = state.candidateBuilds?.find((b) => b.candidate === (state.payloadCandidate ?? ''))
    ?? state.candidateBuilds?.[0];
  return primary?.engine ?? !state.payloadReady;
}

export type TestingRunStatus = 'building' | 'ready' | 'failed' | 'skipped';

// One testing build (testing_buildBlockV1 call) of the slot's primary target.
export interface TestingRunView {
  run: number;
  status: TestingRunStatus;
  startedAt: number; // unix ms
  endAt?: number; // unix ms; absent while building
  // The build the target's payload comes from (the hand-over pick, or the
  // latest upgrade); the target's other builds are drawn dimmed.
  handedOver: boolean;
  // Its payload feeds the bids.
  selected: boolean;
  // Drawn dimmed: another build of the target was handed over.
  dimmed: boolean;
  data: PopoverData;
}

const statusLabel = (status: TestingRunStatus) =>
  status === 'ready' ? 'Built At' : status === 'failed' ? 'Failed At' : 'Skipped At';

function liveRun(live: LocalBuildProgressEvent, slotStartTime: number, dimmed: boolean): TestingRunView {
  const run = live.run ?? 1;
  const items: PopoverItem[] = [
    { label: 'Build', value: `#${run}` },
    { label: 'Started', value: `${live.started_at - slotStartTime}ms` },
    ...(live.phase === 'building'
      ? [{ label: 'Status', value: 'building…' }]
      : [
          { label: statusLabel(live.phase), value: `${live.at - slotStartTime}ms` },
          { label: 'Duration', value: `${live.at - live.started_at}ms` },
          { label: 'Status', value: `${live.phase}${live.skip_reason ? ` (${live.skip_reason})` : ''}` },
        ]),
    ...(live.error ? [{ label: 'Error', value: live.error }] : []),
    { label: 'Tx Source', value: live.tx_source || '—' },
    ...(live.phase === 'ready'
      ? [
          { label: 'Contents', value: `${live.num_transactions ?? 0} txs` },
          { label: 'Block Value', value: gwei(live.block_value_wei) },
        ]
      : []),
  ];

  return {
    run,
    status: live.phase,
    startedAt: live.started_at,
    endAt: live.phase === 'building' ? undefined : live.at,
    handedOver: false,
    selected: false,
    dimmed,
    data: { title: 'Testing Build (testing_buildBlockV1)', items },
  };
}

function finishedRun(run: LocalBuildRun, slotStartTime: number): TestingRunView {
  const startedAt = Date.parse(run.started_at);
  const at = Date.parse(run.at);
  const items: PopoverItem[] = [
    { label: 'Started', value: `${startedAt - slotStartTime}ms` },
    ...(run.selected_at ? [{ label: 'Selection', value: `${Date.parse(run.selected_at) - startedAt}ms` }] : []),
    { label: statusLabel(run.status), value: `${at - slotStartTime}ms` },
    { label: 'Duration', value: `${at - startedAt}ms` },
    { label: 'Status', value: run.status },
    ...(run.error ? [{ label: 'Error', value: run.error }] : []),
    ...(run.status === 'ready'
      ? [
          { label: 'Contents', value: `${run.num_transactions ?? 0} txs, gas ${(run.gas_used ?? 0).toLocaleString()}` },
          { label: 'Block Value', value: gwei(run.block_value_wei) },
        ]
      : []),
  ];

  return {
    run: run.run,
    status: run.status,
    startedAt,
    endAt: at,
    handedOver: false,
    selected: false,
    dimmed: true,
    data: { title: `Testing Build #${run.run} (not handed over)`, items },
  };
}

function handedOverRun(lb: LocalBuildStreamEvent, slot: number, slotStartTime: number): TestingRunView {
  const status = (lb.status as TestingRunStatus) ?? 'skipped';
  const endAt = status === 'ready' ? (lb.built_at || lb.at) : lb.at;
  const startedAt = lb.started_at ?? endAt;
  const startMs = startedAt - slotStartTime;
  const selectionMs = lb.info?.selected_at && lb.started_at ? Date.parse(lb.info.selected_at) - lb.started_at : null;
  const selection = lb.info?.selection;

  const items: PopoverItem[] = [
    ...(lb.info?.builds && lb.info.builds > 1
      ? [{ label: 'Build', value: `#${lb.info.run ?? '?'} of ${lb.info.builds} (the most valuable)` }]
      : []),
    { label: 'Build Start', value: `${startMs}ms` },
    ...(selectionMs !== null ? [{ label: 'Selection', value: `${selectionMs}ms` }] : []),
    ...(status === 'ready' && lb.built_at
      ? [
          { label: 'Built At', value: `${lb.built_at - slotStartTime}ms` },
          { label: 'Duration', value: `${lb.built_at - startedAt}ms` },
        ]
      : [{ label: statusLabel(status), value: `${lb.at - slotStartTime}ms` }]),
    { label: 'Status', value: `${lb.status}${lb.skip_reason ? ` (${lb.skip_reason})` : ''}` },
    ...(lb.error ? [{ label: 'Error', value: lb.error }] : []),
    { label: 'Tx Source', value: lb.tx_source || '—' },
    { label: 'Payload Source', value: lb.payload_source || '—' },
    {
      label: 'Bids With',
      value: lb.selected
        ? lb.payload_source === 'higher_value' ? 'this block (worth more)' : 'this block'
        : lb.fallback ? 'engine block (fallback)'
          : lb.payload_source === 'higher_value' ? 'engine block (worth at least as much)'
            : 'engine block (this one is compare-only)',
    },
    ...(lb.local
      ? [
          { label: 'Block Hash', value: truncate(lb.local.block_hash), copyValue: lb.local.block_hash },
          { label: 'Block Value', value: gwei(lb.local.block_value_wei) },
          { label: 'Contents', value: `${lb.local.num_transactions} txs, ${lb.local.num_blobs} blobs` },
          { label: 'Gas', value: `${lb.local.gas_used.toLocaleString()} / ${lb.local.gas_limit.toLocaleString()}` },
        ]
      : []),
    ...(selection
      ? [
          { label: 'Pool Selection', value: `${selection.selected} of ${selection.pool_size} pooled (gas ${selection.gas_sum.toLocaleString()})` },
          ...(selection.fill_candidates
            ? [{
                label: 'EL Mempool Fill',
                value: `${selection.fill_selected ?? 0} of ${selection.fill_candidates} pending (gas ${(selection.fill_gas_sum ?? 0).toLocaleString()}` +
                  `${selection.fill_blobs ? `, ${selection.fill_blobs} blobs` : ''})` +
                  `${lb.info?.fill_dropped ? ', dropped after an EL refusal' : lb.info?.fill_blobs_dropped ? ', blob transactions dropped after an EL refusal' : ''}`,
              }]
            : []),
          ...(selection.skipped && Object.keys(selection.skipped).length > 0
            ? [{ label: 'Skipped', value: Object.entries(selection.skipped).map(([k, v]) => `${k}: ${v}`).join(', ') }]
            : []),
        ]
      : []),
    ...(lb.info?.fill_error ? [{ label: 'EL Mempool', value: `unreadable: ${lb.info.fill_error}` }] : []),
    ...(lb.info?.dropped_by_el ? [{ label: 'Dropped by EL', value: `${lb.info.dropped_by_el}` }] : []),
    ...(lb.info?.inclusion_list_dropped ? [{ label: 'Inclusion List', value: 'dropped on retry' }] : []),
    ...(lb.el
      ? [
          { label: 'Engine Block Hash', value: truncate(lb.el.block_hash), copyValue: lb.el.block_hash },
          { label: 'Engine Block', value: `${lb.el.num_transactions} txs, ${gwei(lb.el.block_value_wei)}` },
        ]
      : []),
  ];

  return {
    run: lb.info?.run ?? 1,
    status,
    startedAt,
    endAt,
    handedOver: true,
    selected: lb.selected && status === 'ready',
    dimmed: false,
    data: {
      title: 'Testing Build (testing_buildBlockV1)',
      wide: true,
      items,
      artifact: lb.local
        ? {
            url: `/api/buildoor/slot-results/${slot}/payload${lb.selected ? '' : '?source=local'}`,
            filename: `slot-${slot}-payload${lb.selected ? '' : '-local'}.ssz`,
          }
        : undefined,
    },
  };
}

// testingRuns lists every testing build of the slot's primary target, in
// build order: the finished builds of the final local_build record (the
// handed-over one highlighted, the others dimmed) plus builds still running
// after it, or the live builds before any final record.
export function testingRuns(state: SlotState, slot: number, slotStartTime: number): TestingRunView[] {
  const lb = state.localBuild;
  const live = Object.values(state.localBuildLive ?? {});

  if (!lb) {
    return live
      .map((l) => liveRun(l, slotStartTime, false))
      .sort((a, b) => a.run - b.run);
  }

  const handed = handedOverRun(lb, slot, slotStartTime);
  const runs = [
    handed,
    ...(lb.info?.runs ?? [])
      .filter((r) => r.run !== handed.run)
      .map((r) => finishedRun(r, slotStartTime)),
  ];
  const known = new Set(runs.map((r) => r.run));
  for (const l of live) {
    const run = l.run ?? 1;
    if (!known.has(run) && run > handed.run) runs.push(liveRun(l, slotStartTime, true));
  }

  return runs.sort((a, b) => a.run - b.run);
}
