import React from 'react';
import type { SlotPlan, SlotResult } from '../../types';

// Derived cell status, ordered by display priority.
export type SlotCellStatus =
  | 'included' // green: our payload was included at the head
  | 'failed' // red: build failed OR reveal suppressed/skipped/failed
  | 'revealed' // blue: reveal published
  | 'bidding' // yellow: bids submitted/served but no inclusion
  | 'idle' // purple hollow: planned/active but nothing happened
  | 'built' // gray: payload built, nothing further
  | null; // no record

const STATUS_LABELS: Record<Exclude<SlotCellStatus, null>, string> = {
  included: 'Included',
  failed: 'Failed',
  revealed: 'Revealed',
  bidding: 'Bid submitted/served',
  idle: 'Active, nothing happened',
  built: 'Payload built',
};

// deriveSlotCellStatus maps a slot result onto the compact cell indicator.
export function deriveSlotCellStatus(result?: SlotResult): SlotCellStatus {
  if (!result) return null;

  if (result.inclusion) return 'included';

  const reveals = result.reveal_attempts || [];
  const revealPublished = reveals.some((r) => r.status === 'published');
  const revealBad = reveals.some(
    (r) => r.status === 'failed' || r.status === 'suppressed' || r.status === 'skipped'
  );

  if (result.build?.status === 'failed') return 'failed';
  if (revealPublished) return 'revealed';
  if (revealBad) return 'failed';

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
  const status = deriveSlotCellStatus(result);

  const titleParts = [`Slot ${slot}`];
  if (plan?.bid) titleParts.push(`bid: ${plan.bid.mode}`);
  if (plan?.builder_api) titleParts.push(`builder api: ${plan.builder_api.mode}`);
  if (plan?.reveal) titleParts.push(`reveal: ${plan.reveal.mode}`);
  if (status) titleParts.push(STATUS_LABELS[status]);

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
        </span>
        {status && <span className={`ap-dot ap-dot-${status}`}></span>}
      </button>
    </td>
  );
};

export const SlotCell = React.memo(SlotCellInner);
