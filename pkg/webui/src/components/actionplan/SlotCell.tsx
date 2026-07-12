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

const chipClass = (mode?: 'custom' | 'disabled'): string =>
  mode === 'disabled' ? 'ap-chip ap-chip-disabled' : 'ap-chip ap-chip-custom';

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
  const t = plan?.transforms;
  const hasTransform = !!(t && (t.payload || t.bid || t.envelope));

  const titleParts = [`Slot ${slot}`];
  if (plan?.bid) titleParts.push(`bid: ${plan.bid.mode}`);
  if (plan?.builder_api) titleParts.push(`builder api: ${plan.builder_api.mode}`);
  if (plan?.reveal) titleParts.push(`reveal: ${plan.reveal.mode}`);
  if (reorgParent) titleParts.push('build: reorg parent (n-2)');
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
          {plan?.bid && <span className={chipClass(plan.bid.mode)}>B</span>}
          {plan?.builder_api && <span className={chipClass(plan.builder_api.mode)}>A</span>}
          {plan?.reveal && <span className={chipClass(plan.reveal.mode)}>R</span>}
          {reorgParent && <span className="ap-chip ap-chip-reorg" title="Build on n-2 payload">P</span>}
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
