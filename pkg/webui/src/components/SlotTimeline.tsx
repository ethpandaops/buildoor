import React, { useState, useEffect, useRef, useCallback } from 'react';
import type { SlotState, Config, ChainInfo, ServiceStatus, LegacyBuilderInfo } from '../types';
import { SlotGraph } from './SlotGraph';

interface SlotTimelineProps {
  chainInfo: ChainInfo | null;
  slotStates: Record<number, SlotState>;
  slotConfigs: Record<number, Config>;
  currentConfig: Config | null;
  serviceStatus: ServiceStatus | null;
  legacyBuilderInfo: LegacyBuilderInfo | null;
}

const MAX_SLOTS_TO_SHOW = 5;

interface SlotDisplayState {
  displaySlot: number;
  showNextSlot: boolean;
}

export const SlotTimeline: React.FC<SlotTimelineProps> = ({
  chainInfo,
  slotStates,
  slotConfigs,
  currentConfig,
  serviceStatus,
  legacyBuilderInfo
}) => {
  const [slotDisplay, setSlotDisplay] = useState<SlotDisplayState>({ displaySlot: 0, showNextSlot: false });
  const firstValidSlotRef = useRef<number>(-1);
  const animationRef = useRef<number>(0);

  const calculateSlotDisplay = useCallback((): SlotDisplayState => {
    if (!chainInfo || chainInfo.genesis_time === 0) {
      return { displaySlot: 0, showNextSlot: false };
    }
    const now = Date.now();
    const slotDuration = chainInfo.seconds_per_slot;
    const elapsed = now - chainInfo.genesis_time;
    const displaySlot = Math.floor(elapsed / slotDuration);
    const msIntoCurrentSlot = elapsed % slotDuration;
    const showNextSlot = msIntoCurrentSlot >= slotDuration * 0.75;
    return { displaySlot, showNextSlot };
  }, [chainInfo]);

  // Check for slot changes using requestAnimationFrame but only update state when needed
  useEffect(() => {
    if (!chainInfo || chainInfo.genesis_time === 0) return;

    let lastDisplaySlot = slotDisplay.displaySlot;
    let lastShowNextSlot = slotDisplay.showNextSlot;

    const checkSlotChange = () => {
      const { displaySlot, showNextSlot } = calculateSlotDisplay();

      // Only trigger re-render if the slot display needs to change
      if (displaySlot !== lastDisplaySlot || showNextSlot !== lastShowNextSlot) {
        lastDisplaySlot = displaySlot;
        lastShowNextSlot = showNextSlot;
        setSlotDisplay({ displaySlot, showNextSlot });
      }

      animationRef.current = requestAnimationFrame(checkSlotChange);
    };

    // Initialize
    const initial = calculateSlotDisplay();
    setSlotDisplay(initial);
    lastDisplaySlot = initial.displaySlot;
    lastShowNextSlot = initial.showNextSlot;

    animationRef.current = requestAnimationFrame(checkSlotChange);

    return () => {
      if (animationRef.current) {
        cancelAnimationFrame(animationRef.current);
      }
    };
  }, [chainInfo, calculateSlotDisplay]);

  if (!chainInfo || chainInfo.genesis_time === 0) {
    return (
      <div className="slot-graphs-container">
        <div className="text-muted p-3">Waiting for chain info...</div>
      </div>
    );
  }

  const { displaySlot, showNextSlot } = slotDisplay;

  // Track first valid slot
  if (firstValidSlotRef.current < 0 && displaySlot > 0) {
    firstValidSlotRef.current = displaySlot;
  }

  // Calculate how many slots to show
  const slotsSinceStart = displaySlot - (firstValidSlotRef.current >= 0 ? firstValidSlotRef.current : displaySlot) + 1;
  let slotsToShow = Math.min(Math.max(1, slotsSinceStart), MAX_SLOTS_TO_SHOW);

  const topSlot = showNextSlot ? displaySlot + 1 : displaySlot;

  if (showNextSlot && slotsToShow < MAX_SLOTS_TO_SHOW) {
    slotsToShow++;
  }

  const slots: number[] = [];
  for (let i = 0; i < slotsToShow; i++) {
    const slot = topSlot - i;
    if (slot >= 0) {
      slots.push(slot);
    }
  }

  return (
    <div className="slot-graphs-container">
      {slots.map(slot => (
        <SlotGraph
          key={slot}
          slot={slot}
          state={slotStates[slot] || { slot }}
          originalConfig={slotConfigs[slot] || null}
          currentConfig={currentConfig}
          chainInfo={chainInfo}
          currentDisplaySlot={displaySlot}
          serviceStatus={serviceStatus}
          legacyBuilderInfo={legacyBuilderInfo}
        />
      ))}
    </div>
  );
};
