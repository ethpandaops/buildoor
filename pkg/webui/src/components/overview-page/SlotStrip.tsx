import React, { useMemo } from 'react';
import { STRIP_SLOTS } from './useOverviewData';
import type { BidWonEntry, SlotResult, SlotState } from '../../types';

type SlotKind = 'won' | 'built' | 'none';

interface SlotStripProps {
  currentSlot: number;
  slotStates: Record<number, SlotState>;
  results: Record<number, SlotResult>;
  recentWins: BidWonEntry[];
}

function classify(
  slot: number,
  state: SlotState | undefined,
  result: SlotResult | undefined,
  wonSlots: Set<number>,
): SlotKind {
  if (wonSlots.has(slot) || state?.bidWon || result?.inclusion) return 'won';
  if (state?.payloadReady) return 'built';
  if (result?.build?.status === 'ready' || result?.builds?.some((b) => b.status === 'ready')) return 'built';
  return 'none';
}

const KIND_LABEL: Record<SlotKind, string> = {
  won: 'won',
  built: 'built',
  none: '—',
};

const KIND_DESCRIPTION: Record<SlotKind, string> = {
  won: 'won by buildoor',
  built: 'payload built',
  none: 'not built',
};

export const SlotStrip: React.FC<SlotStripProps> = ({ currentSlot, slotStates, results, recentWins }) => {
  const wonSlots = useMemo(() => new Set(recentWins.map((w) => w.slot)), [recentWins]);

  const slots = useMemo(() => {
    if (currentSlot <= 0) return [];
    const first = Math.max(0, currentSlot - STRIP_SLOTS + 1);
    const list: { slot: number; kind: SlotKind }[] = [];
    for (let slot = first; slot <= currentSlot; slot++) {
      list.push({ slot, kind: classify(slot, slotStates[slot], results[slot], wonSlots) });
    }
    return list;
  }, [currentSlot, slotStates, results, wonSlots]);

  return (
    <section className="card" aria-labelledby="ov-strip-title">
      <div className="card-body">
        <div className="ov-section-head">
          <h2 id="ov-strip-title" className="bd-card-title">
            Last {STRIP_SLOTS} slots
          </h2>
          {currentSlot > 0 && <span className="bd-muted ov-small">current slot {currentSlot.toLocaleString()}</span>}
        </div>
        {slots.length === 0 ? (
          <p className="bd-muted ov-small mb-0">Waiting for the chain clock…</p>
        ) : (
          <ol className="ov-strip">
            {slots.map(({ slot, kind }) => {
              const current = slot === currentSlot;
              const description = current ? `current slot, ${KIND_DESCRIPTION[kind]} so far` : KIND_DESCRIPTION[kind];
              return (
                <li
                  key={slot}
                  className={`ov-slot ov-slot-${kind}${current ? ' ov-slot-current' : ''}`}
                  title={`Slot ${slot}: ${description}`}
                  aria-label={`Slot ${slot}: ${description}`}
                >
                  <span className="ov-slot-num">{slot % 1000}</span>
                  <span className="ov-slot-label">{current && kind === 'none' ? 'now' : KIND_LABEL[kind]}</span>
                </li>
              );
            })}
          </ol>
        )}
        <ul className="ov-legend" aria-label="Legend">
          <li>
            <span className="ov-swatch ov-slot-won" aria-hidden="true" /> won by buildoor
          </li>
          <li>
            <span className="ov-swatch ov-slot-built" aria-hidden="true" /> payload built
          </li>
          <li>
            <span className="ov-swatch ov-slot-none" aria-hidden="true" /> not built / no record
          </li>
          <li>
            <span className="ov-swatch ov-slot-none ov-slot-current" aria-hidden="true" /> current slot
          </li>
        </ul>
      </div>
    </section>
  );
};
