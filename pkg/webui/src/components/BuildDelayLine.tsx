import React, { useLayoutEffect, useRef } from 'react';

interface BuildDelayLineProps {
  leftPct: number;            // build start position, in %
  slotStartTime: number;
  rangeStart: number;
  totalRange: number;
  endAt?: number;             // absolute ms; when set, the line is finalized (success)
  expectedEndAt?: number;     // absolute ms; caps the in-progress right edge
  onClick: (e: React.MouseEvent) => void;
  // Base CSS class; "<className>-active" is appended while in progress.
  // Defaults to the payload build span style.
  className?: string;
  // Overrides the line color (build candidate identity).
  lineColor?: string;
}

// BuildDelayLine renders the payload build span on the slot timeline.
// While the build is in progress (endAt unset) its right edge grows toward
// the current time: the line is laid out at its final extent (the expected
// end, else the end of the graph) and a linear horizontal scale animation
// from "now" to that extent runs on the compositor, so the growth needs no
// script per frame. Once the build finalizes it renders a static line from
// build start to the end time. Build failures are rendered as a red dot by
// the parent instead — a thin line is too hard to see and click.
export const BuildDelayLine: React.FC<BuildDelayLineProps> = ({
  leftPct,
  slotStartTime,
  rangeStart,
  totalRange,
  endAt,
  expectedEndAt,
  onClick,
  className = 'build-delay-line',
  lineColor
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const left = Math.max(0, leftPct);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;

    const pctAt = (at: number) => ((at - slotStartTime - rangeStart) / totalRange) * 100;
    const show = (rightPct: number): number => {
      const width = Math.min(100, rightPct) - left;
      if (width > 0) {
        el.style.left = `${left}%`;
        el.style.width = `${width}%`;
        el.style.display = 'block';
      } else {
        el.style.display = 'none';
      }
      return width;
    };

    // Finalized build: static line from build start to the end time.
    if (endAt) {
      show(pctAt(endAt));
      return;
    }

    // In progress: lay out the full extent and grow into it in real time.
    const now = Date.now();
    const capAt = expectedEndAt ?? slotStartTime + rangeStart + totalRange;
    const width = show(pctAt(capAt));
    if (width <= 0) return;

    const progress = Math.min(1, Math.max(0, (pctAt(now) - left) / width));
    const remaining = capAt - now;
    if (remaining <= 0 || progress >= 1) return;

    const animation = el.animate(
      // `scale`, not `transform`: the line CSS centers it with a translate.
      [{ scale: `${progress} 1` }, { scale: '1 1' }],
      { duration: remaining, easing: 'linear', fill: 'forwards' },
    );

    return () => animation.cancel();
  }, [left, slotStartTime, rangeStart, totalRange, endAt, expectedEndAt]);

  // Styling: finalized (plain) vs in-progress (pulsing).
  const stateClass = endAt ? '' : ` ${className}-active`;

  return (
    <div
      ref={ref}
      className={`${className}${stateClass}`}
      style={{ transformOrigin: 'left center', ...(lineColor ? { backgroundColor: lineColor } : {}) }}
      onClick={onClick}
    />
  );
};
