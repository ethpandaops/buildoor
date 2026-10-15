import React, { useRef, useEffect } from 'react';
import { onFrame } from '../utils/frameClock';

interface CurrentTimeIndicatorProps {
  slotStartTime: number;
  rangeStart: number;
  totalRange: number;
}

// CurrentTimeIndicator moves the "now" line across a slot graph. It spans the
// whole graph and is shifted with a transform (a percentage of its own width
// = the graph width), so a frame only touches the compositor, and it writes
// to the DOM only when the line is visible or its visibility changes.
export const CurrentTimeIndicator: React.FC<CurrentTimeIndicatorProps> = ({
  slotStartTime,
  rangeStart,
  totalRange
}) => {
  const trackRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let visible: boolean | null = null;

    return onFrame((now) => {
      const track = trackRef.current;
      if (!track) return;

      const position = ((now - slotStartTime - rangeStart) / totalRange) * 100;
      const inRange = position >= 0 && position <= 100;

      if (inRange !== visible) {
        visible = inRange;
        track.style.visibility = inRange ? 'visible' : 'hidden';
      }
      if (inRange) {
        track.style.transform = `translateX(${position}%)`;
      }
    });
  }, [slotStartTime, rangeStart, totalRange]);

  return (
    <div ref={trackRef} className="current-time-track" style={{ visibility: 'hidden' }}>
      <div className="current-time-indicator" />
    </div>
  );
};
