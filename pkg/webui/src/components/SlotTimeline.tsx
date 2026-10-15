import React, { useState, useEffect, useLayoutEffect, useRef, useCallback, useMemo } from 'react';
import type { SlotState, Config, ChainInfo, ServiceStatus } from '../types';
import { SlotGraph, slotGraphHeight } from './SlotGraph';
import { SLOT_HISTORY } from '../hooks/useEventStream';

interface SlotTimelineProps {
  chainInfo: ChainInfo | null;
  slotStates: Record<number, SlotState>;
  slotConfigs: Record<number, Config>;
  slotServiceStatuses: Record<number, ServiceStatus>;
  currentConfig: Config | null;
  serviceStatus: ServiceStatus | null;
  hideHeadVotes?: boolean;
  // Height of the scrollable window in pixels.
  viewportHeight?: number;
}

// Gap between two slot graphs (matches .slot-graphs-container).
const SLOT_GAP = 2;
// Slots rendered beyond the visible ones in each direction.
const OVERSCAN = 2;
// The window counts as "at now" while scrolled less than this from the top.
const FOLLOW_TOLERANCE = 4;

interface SlotDisplayState {
  displaySlot: number;
  showNextSlot: boolean;
}

// SlotTimeline shows a rolling window of up to SLOT_HISTORY slots, newest on
// top, in a scrollable viewport. Only the slots in (or near) the viewport
// render their graph; the others keep their exact height as an empty spacer,
// so scrolling stays smooth with a full window. While scrolled to the top the
// view follows new slots; scrolled down, it stays on the slots being read.
export const SlotTimeline: React.FC<SlotTimelineProps> = ({
  chainInfo,
  slotStates,
  slotConfigs,
  slotServiceStatuses,
  currentConfig,
  serviceStatus,
  hideHeadVotes,
  viewportHeight = 420,
}) => {
  const [slotDisplay, setSlotDisplay] = useState<SlotDisplayState>({ displaySlot: 0, showNextSlot: false });
  const animationRef = useRef<number>(0);
  const viewportRef = useRef<HTMLDivElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const firstSlotRef = useRef<number>(-1);

  const calculateSlotDisplay = useCallback((): SlotDisplayState => {
    if (!chainInfo || chainInfo.genesis_time === 0) {
      return { displaySlot: 0, showNextSlot: false };
    }
    const slotDuration = chainInfo.seconds_per_slot;
    const elapsed = Date.now() - chainInfo.genesis_time;
    const displaySlot = Math.floor(elapsed / slotDuration);
    const showNextSlot = elapsed % slotDuration >= slotDuration * 0.75;
    return { displaySlot, showNextSlot };
  }, [chainInfo]);

  // Check for slot changes every frame, but only re-render when the window
  // actually moves (new slot, or the next slot appearing at 75%).
  useEffect(() => {
    if (!chainInfo || chainInfo.genesis_time === 0) return;

    const initial = calculateSlotDisplay();
    setSlotDisplay(initial);
    let last = initial;

    const checkSlotChange = () => {
      const next = calculateSlotDisplay();
      if (next.displaySlot !== last.displaySlot || next.showNextSlot !== last.showNextSlot) {
        last = next;
        setSlotDisplay(next);
      }
      animationRef.current = requestAnimationFrame(checkSlotChange);
    };
    animationRef.current = requestAnimationFrame(checkSlotChange);

    return () => cancelAnimationFrame(animationRef.current);
  }, [chainInfo, calculateSlotDisplay]);

  const { displaySlot, showNextSlot } = slotDisplay;
  const topSlot = showNextSlot ? displaySlot + 1 : displaySlot;

  // The window starts at the oldest slot the stream delivered (the connect
  // replay covers a few slots; history grows while the page stays open).
  if (displaySlot > 0) {
    let first = firstSlotRef.current >= 0 ? firstSlotRef.current : displaySlot;
    for (const key in slotStates) {
      const slot = Number(key);
      if (slot < first) first = slot;
    }
    firstSlotRef.current = Math.max(first, topSlot - SLOT_HISTORY + 1);
  }

  const slots = useMemo(() => {
    const list: number[] = [];
    if (displaySlot <= 0) return list;
    const oldest = Math.max(0, firstSlotRef.current >= 0 ? firstSlotRef.current : displaySlot);
    for (let slot = topSlot; slot >= oldest; slot--) list.push(slot);
    return list;
    // firstSlotRef only moves together with displaySlot/slotStates.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [topSlot, displaySlot, slotStates]);

  // Height of each slot's graph: future slots follow the live service status,
  // past ones the status captured when they started (as SlotGraph does).
  const heights = useMemo(
    () =>
      slots.map((slot) =>
        slotGraphHeight(slot > displaySlot ? serviceStatus : slotServiceStatuses[slot] || serviceStatus),
      ),
    [slots, displaySlot, serviceStatus, slotServiceStatuses],
  );

  const offsets = useMemo(() => {
    const out: number[] = [];
    let y = 0;
    for (const h of heights) {
      out.push(y);
      y += h + SLOT_GAP;
    }
    return out;
  }, [heights]);

  // Keep the reader's place when slots are added on top while scrolled down.
  const prevTopRef = useRef(topSlot);
  useLayoutEffect(() => {
    const viewport = viewportRef.current;
    const added = topSlot - prevTopRef.current;
    prevTopRef.current = topSlot;
    if (!viewport || added <= 0 || viewport.scrollTop <= FOLLOW_TOLERANCE) return;
    let shift = 0;
    for (let i = 0; i < Math.min(added, heights.length); i++) shift += heights[i] + SLOT_GAP;
    viewport.scrollTop += shift;
    setScrollTop(viewport.scrollTop);
  }, [topSlot, heights]);

  if (!chainInfo || chainInfo.genesis_time === 0) {
    return (
      <div className="slot-graphs-container">
        <div className="text-muted p-3">Waiting for chain info...</div>
      </div>
    );
  }

  const viewBottom = scrollTop + viewportHeight;
  let firstVisible = 0;
  while (firstVisible < slots.length - 1 && offsets[firstVisible] + heights[firstVisible] < scrollTop) firstVisible++;
  let lastVisible = firstVisible;
  while (lastVisible < slots.length - 1 && offsets[lastVisible + 1] < viewBottom) lastVisible++;
  const renderFrom = Math.max(0, firstVisible - OVERSCAN);
  const renderTo = Math.min(slots.length - 1, lastVisible + OVERSCAN);
  const following = scrollTop <= FOLLOW_TOLERANCE;

  return (
    <div className="slot-timeline">
      <div
        ref={viewportRef}
        className="slot-timeline-viewport"
        style={{ maxHeight: `${viewportHeight}px` }}
        onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
      >
        <div className="slot-graphs-container">
          {slots.map((slot, index) =>
            index >= renderFrom && index <= renderTo ? (
              <SlotGraph
                key={slot}
                slot={slot}
                state={slotStates[slot] || { slot }}
                originalConfig={slotConfigs[slot] || null}
                originalServiceStatus={slotServiceStatuses[slot] || null}
                currentConfig={currentConfig}
                chainInfo={chainInfo}
                currentDisplaySlot={displaySlot}
                serviceStatus={serviceStatus}
                hideHeadVotes={hideHeadVotes}
              />
            ) : (
              <div key={slot} className="slot-graph-spacer" style={{ height: `${heights[index]}px` }} />
            ),
          )}
        </div>
      </div>
      {!following && (
        <button
          type="button"
          className="bd-btn bd-btn-sm slot-timeline-now"
          onClick={() => viewportRef.current?.scrollTo({ top: 0, behavior: 'smooth' })}
        >
          <i className="fas fa-arrow-up" aria-hidden="true"></i>
          Back to now
        </button>
      )}
    </div>
  );
};
