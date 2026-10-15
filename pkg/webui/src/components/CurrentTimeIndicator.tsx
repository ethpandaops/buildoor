import React, { useLayoutEffect, useRef } from 'react';

interface CurrentTimeIndicatorProps {
  slotStartTime: number;
  rangeStart: number;
  totalRange: number;
}

// CurrentTimeIndicator sweeps the "now" line across a slot graph as one
// linear transform animation over the graph's whole time range, started at
// the offset "now" has into it (a negative delay when the range began
// already). The browser runs it on the compositor: no script per frame, and
// main-thread work (React renders, stream events) cannot stall it. Outside
// the range the track rests off-graph (clipped by the graph area).
export const CurrentTimeIndicator: React.FC<CurrentTimeIndicatorProps> = ({
  slotStartTime,
  rangeStart,
  totalRange
}) => {
  const trackRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const track = trackRef.current;
    if (!track || totalRange <= 0) return;

    const animation = track.animate(
      [{ transform: 'translateX(0%)' }, { transform: 'translateX(100%)' }],
      {
        duration: totalRange,
        delay: slotStartTime + rangeStart - Date.now(),
        easing: 'linear',
        fill: 'none',
      },
    );

    return () => animation.cancel();
  }, [slotStartTime, rangeStart, totalRange]);

  return (
    <div ref={trackRef} className="current-time-track">
      <div className="current-time-indicator" />
    </div>
  );
};
