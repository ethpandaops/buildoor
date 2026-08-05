import React from 'react';
import type { SlotPlan, SlotResult } from '../../types';

// Left dot: bid outcome, ordered by display priority.
export type BidDotStatus =
  | 'included' // green: our bid won (block committed to our payload)
  | 'orphaned' // hollow red: won, but the block was reorged out
  | 'failed' // red: build failed
  | 'bidding' // yellow: bids submitted/served but not included
  | 'built' // gray: payload built, nothing further
  | 'idle' // purple hollow: planned/active but nothing happened
  | null; // no record

// Right dot: canonical payload verdict, only rendered for won slots.
export type PayloadDotStatus = 'canonical' | 'missed' | 'orphaned' | 'pending' | null;

const BID_LABELS: Record<Exclude<BidDotStatus, null>, string> = {
  included: 'Bid included',
  orphaned: 'Bid reorged out',
  failed: 'Failed',
  bidding: 'Bid submitted/served, not included',
  built: 'Payload built',
  idle: 'Active, nothing happened',
};

const PAYLOAD_LABELS: Record<Exclude<PayloadDotStatus, null>, string> = {
  canonical: 'Payload canonical',
  missed: 'Payload missed',
  orphaned: 'Block orphaned',
  pending: 'Payload verdict pending',
};

// deriveBidStatus maps a slot result onto the left (bid outcome) dot.
export function deriveBidStatus(result?: SlotResult): BidDotStatus {
  if (!result) return null;

  if (result.inclusion) {
    return result.inclusion.payload_status === 'orphaned' ? 'orphaned' : 'included';
  }

  if (result.build?.status === 'failed') return 'failed';

  const bids = result.bids || [];
  if (bids.some((b) => b.status === 'submitted' || b.status === 'served')) return 'bidding';

  const buildStatus = result.build?.status;
  if (buildStatus === 'ready') return 'built';
  if (
    buildStatus === 'no_attributes' ||
    buildStatus === 'waiting_attributes' ||
    buildStatus === 'started'
  ) {
    return 'idle';
  }

  return null;
}

// derivePayloadStatus maps a won slot's canonical verdict onto the right dot.
// Only won slots get a payload dot — without a win there is nothing to reveal.
export function derivePayloadStatus(result?: SlotResult): PayloadDotStatus {
  if (!result?.inclusion) return null;

  return result.inclusion.payload_status || 'pending';
}

// describeReveal summarizes the slot's reveal attempts for the tooltip.
function describeReveal(result?: SlotResult): string | null {
  const reveals = result?.reveal_attempts || [];
  if (reveals.length === 0) return null;

  if (reveals.some((r) => r.status === 'published')) return 'revealed';
  if (reveals.some((r) => r.status === 'suppressed' || r.status === 'skipped')) return 'reveal withheld';
  if (reveals.some((r) => r.status === 'failed')) return 'reveal failed';

  return null;
}

interface SlotCellProps {
  slot: number;
  plan?: SlotPlan;
  result?: SlotResult;
  isCurrent: boolean;
  selected: boolean;
  onCellClick: (slot: number, shiftKey: boolean) => void;
}

// Rule-derived plans are drawn dashed: they are not stored for the slot and a
// rule change (or an explicit plan) can still replace them.
const chipClass = (mode: 'custom' | 'disabled' | undefined, fromRule: boolean): string =>
  `ap-chip ${mode === 'disabled' ? 'ap-chip-disabled' : 'ap-chip-custom'}${fromRule ? ' ap-chip-rule' : ''}`;

const SlotCellInner: React.FC<SlotCellProps> = ({
  slot,
  plan,
  result,
  isCurrent,
  selected,
  onCellClick,
}) => {
  const bidStatus = deriveBidStatus(result);
  const payloadStatus = derivePayloadStatus(result);

  const reorgParent = plan?.build?.reorg_parent_payload === true;
  const candidateOverrides = Object.keys(plan?.build?.candidates ?? {}).length > 0;
  const t = plan?.transforms;
  const hasTransform = !!(t && (t.payload || t.bid || t.envelope));

  const fromRule = !!plan?.rule_id;

  const titleParts = [`Slot ${slot}`];
  if (fromRule) titleParts.push(`rule: ${plan?.rule_id}`);
  if (plan?.ignore_rules) titleParts.push('recurring rules ignored');
  if (plan?.bid) titleParts.push(`bid: ${plan.bid.mode}`);
  if (plan?.builder_api) titleParts.push(`builder api: ${plan.builder_api.mode}`);
  if (plan?.reveal) titleParts.push(`reveal: ${plan.reveal.mode}`);
  if (reorgParent) titleParts.push('build: reorg parent (n-2)');
  if (candidateOverrides) titleParts.push('build: candidate overrides');
  if (hasTransform) {
    const targets = ['payload', 'bid', 'envelope'].filter((k) => t?.[k as keyof typeof t]);
    titleParts.push(`jq transform: ${targets.join(', ')}`);
  }
  if (bidStatus) titleParts.push(BID_LABELS[bidStatus]);
  if (payloadStatus) titleParts.push(PAYLOAD_LABELS[payloadStatus]);

  const revealSummary = describeReveal(result);
  if (revealSummary) titleParts.push(revealSummary);

  return (
    <td className="ap-cell-td">
      <button
        type="button"
        className={`ap-cell ${isCurrent ? 'ap-current' : ''} ${selected ? 'ap-selected' : ''}`}
        title={titleParts.join(' | ')}
        onClick={(e) => onCellClick(slot, e.shiftKey)}
      >
        <span className="ap-chips">
          {plan?.bid && <span className={chipClass(plan.bid.mode, fromRule)}>B</span>}
          {plan?.builder_api && <span className={chipClass(plan.builder_api.mode, fromRule)}>A</span>}
          {plan?.reveal && <span className={chipClass(plan.reveal.mode, fromRule)}>R</span>}
          {reorgParent && <span className="ap-chip ap-chip-reorg" title="Build on n-2 payload">P</span>}
          {candidateOverrides && <span className="ap-chip ap-chip-candidates" title="Candidate policy overrides">C</span>}
          {hasTransform && <span className="ap-chip ap-chip-transform" title="jq transform">jq</span>}
        </span>
        <span className="ap-dots">
          {bidStatus && <span className={`ap-dot ap-dot-${bidStatus}`}></span>}
          {payloadStatus && <span className={`ap-dot ap-dot-payload-${payloadStatus}`}></span>}
        </span>
      </button>
    </td>
  );
};

export const SlotCell = React.memo(SlotCellInner);
