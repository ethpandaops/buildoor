import React from 'react';
import type { SlotPlan, SlotResult } from '../../types';
import { SlotCell } from './SlotCell';

interface ActionPlanGridProps {
  startEpoch: number;
  endEpoch: number;
  slotsPerEpoch: number;
  currentSlot: number;
  plans: Record<number, SlotPlan>;
  results: Record<number, SlotResult>;
  selection: Set<number>;
  onCellClick: (slot: number, shiftKey: boolean) => void;
}

export const ActionPlanGrid: React.FC<ActionPlanGridProps> = ({
  startEpoch,
  endEpoch,
  slotsPerEpoch,
  currentSlot,
  plans,
  results,
  selection,
  onCellClick,
}) => {
  const currentEpoch = Math.floor(currentSlot / slotsPerEpoch);

  // Rows are epochs DESCENDING: future epochs on top.
  const epochs: number[] = [];
  for (let epoch = endEpoch; epoch >= startEpoch; epoch--) {
    epochs.push(epoch);
  }

  const slotIndices = Array.from({ length: slotsPerEpoch }, (_, i) => i);

  return (
    <div className="ap-grid-scroll">
      <table className="ap-grid">
        <thead>
          <tr>
            <th className="ap-epoch-col text-muted">Epoch</th>
            {slotIndices.map((i) => (
              <th key={i} className="ap-slot-head text-muted">
                {i}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {epochs.map((epoch) => {
            const isFuture = epoch > currentEpoch;
            return (
              <tr key={epoch} className={isFuture ? 'ap-row-future' : ''}>
                <th className="ap-epoch-col font-monospace">
                  {epoch}
                  {epoch === currentEpoch && <i className="fas fa-caret-left ms-1 text-primary"></i>}
                </th>
                {slotIndices.map((i) => {
                  const slot = epoch * slotsPerEpoch + i;
                  return (
                    <SlotCell
                      key={slot}
                      slot={slot}
                      plan={plans[slot]}
                      result={results[slot]}
                      isCurrent={slot === currentSlot}
                      selected={selection.has(slot)}
                      onCellClick={onCellClick}
                    />
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
};
