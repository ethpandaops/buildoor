import type {
  BuildRef,
  BuildTargetRecord,
  PayloadInfo,
  SelectionState,
  SlotBuild,
  SlotState,
  TargetState,
  TargetStatus,
} from './types';
import { apiPath } from './utils/api';

// A slot's builds as separate entities: every payload is kept once under its
// block hash, every target holds its own engine build, testing runs and
// selections, and a selection only names a payload by hash. Nothing here
// merges one build's data into another's.

const CANDIDATE_RANK: Record<string, number> = {
  parent_full: 0,
  parent_empty: 1,
  grandparent_full: 2,
  grandparent_empty: 3,
};

const STATUS_RANK: Record<TargetStatus, number> = {
  selected: 0,
  building: 10,
  no_payload: 20,
  superseded: 30,
};

// targetRank orders targets like the backend's primary target: the most
// canonical one with a selection, then running ones, then the rest.
const targetRank = (status: TargetStatus, candidate?: string): number =>
  STATUS_RANK[status] + (candidate !== undefined && candidate in CANDIDATE_RANK ? CANDIDATE_RANK[candidate] : 4);

export const candidateRank = (candidate?: string): number =>
  candidate !== undefined && candidate in CANDIDATE_RANK ? CANDIDATE_RANK[candidate] : 4;

// primaryTarget returns the slot's primary build target (undefined before any
// target started).
export function primaryTarget(state: SlotState | undefined): TargetState | undefined {
  let best: TargetState | undefined;

  for (const target of Object.values(state?.targets ?? {})) {
    if (!best || targetRank(target.status, target.candidate) < targetRank(best.status, best.candidate)) {
      best = target;
    }
  }

  return best;
}

export const lastSelection = (target: TargetState | undefined): SelectionState | undefined =>
  target?.selections[target.selections.length - 1];

// selectedPayload returns the payload the slot's primary target feeds its
// bids with now.
export function selectedPayload(state: SlotState | undefined): PayloadInfo | undefined {
  const selection = lastSelection(primaryTarget(state));
  return selection ? state?.payloads?.[selection.payloadHash] : undefined;
}

// slotHasSelection reports whether any target of the slot selected a payload.
export const slotHasSelection = (state: SlotState | undefined): boolean =>
  Object.values(state?.targets ?? {}).some((t) => t.status === 'selected');

// payloadByHash finds one of the slot's payloads by block hash (any case).
export function payloadByHash(state: SlotState | undefined, hash: string | undefined): PayloadInfo | undefined {
  if (!hash || !state?.payloads) return undefined;
  return state.payloads[hash] ?? state.payloads[hash.toLowerCase()];
}

// sameBuild reports whether two refs name the same build. A pick names the
// build it took (a rebuild of the same block is another build), so builds
// are matched by ref, never by their payload's hash.
export const sameBuild = (a: BuildRef | undefined, b: BuildRef | undefined): boolean =>
  !!a && !!b && a.kind === b.kind && (a.kind === 'engine' || a.run === b.run);

// buildLabel names a build: "engine" or "testing #3".
export const buildLabel = (build: BuildRef | undefined): string =>
  !build ? 'unknown build' : build.kind === 'engine' ? 'engine' : `testing #${build.run ?? '?'}`;

// primaryTargetRecord is primaryTarget for a slot result's build record.
export function primaryTargetRecord(build: SlotBuild | undefined): BuildTargetRecord | undefined {
  let best: BuildTargetRecord | undefined;

  for (const target of build?.targets ?? []) {
    if (!best || targetRank(target.status, target.candidate) < targetRank(best.status, best.candidate)) {
      best = target;
    }
  }

  return best;
}

// payloadArtifactUrl is the download URL of one of a slot's payloads.
export const payloadArtifactUrl = (slot: number, blockHash: string): string =>
  apiPath(`/slot-results/${slot}/payloads/${blockHash}`);
