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
  // The finalized line's actual request spans (absolute ms): drawn solid,
  // the waits between them dashed.
  calls?: { start: number; end: number }[];
  // Draw the in-progress line dashed: the span is mostly a wait (the engine
  // build between its two calls).
  dashed?: boolean;
  // A finalized line ending in a dot stops this many px short of its end,
  // so it does not run into (or past) the dot.
  endGapPx?: number;
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
  lineColor,
  calls,
  dashed,
  endGapPx = 0
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

    // Finalized build: static line from build start to the end time,
    // clipped short of the end dot (call segments included).
    if (endAt) {
      show(pctAt(endAt));
      el.style.clipPath = endGapPx > 0 ? `inset(-3px ${endGapPx}px -3px 0)` : '';
      return;
    }

    el.style.clipPath = '';

    // In progress: lay out the full extent and grow into it in real time.
    const now = Date.now();
    const capAt = expectedEndAt ?? slotStartTime + rangeStart + totalRange;
    const width = show(pctAt(capAt));
    if (width <= 0) return;

    const progress = Math.min(1, Math.max(0, (pctAt(now) - left) / width));
    const remaining = capAt - now;
    if (remaining <= 0 || progress >= 1) return;

    // Revealed from the left by a clip, so a dashed pattern keeps its
    // spacing while the line grows (a scale would stretch it).
    const animation = el.animate(
      [{ clipPath: `inset(-3px ${(1 - progress) * 100}% -3px 0)` }, { clipPath: 'inset(-3px 0% -3px 0)' }],
      { duration: remaining, easing: 'linear', fill: 'forwards' },
    );

    return () => animation.cancel();
  }, [left, slotStartTime, rangeStart, totalRange, endAt, expectedEndAt, endGapPx]);

  // Styling: finalized (plain) vs in-progress (pulsing).
  const stateClass = endAt ? '' : ` ${className}-active`;

  // A finalized line with known requests: the span is the dashed wait, each
  // request a solid segment on it (positioned within the span).
  const pctAt = (at: number) => ((at - slotStartTime - rangeStart) / totalRange) * 100;
  const spanWidth = endAt ? Math.min(100, pctAt(endAt)) - left : 0;
  const segments = endAt && calls && spanWidth > 0
    ? calls
      .map((call) => {
        const from = Math.max(pctAt(call.start), left);
        const to = Math.min(pctAt(call.end), left + spanWidth);
        return { left: ((from - left) / spanWidth) * 100, width: Math.max(((to - from) / spanWidth) * 100, 0.8) };
      })
      .filter((segment) => segment.left < 100)
    : null;
  const color = lineColor ?? 'rgba(77, 141, 255, 0.9)';

  if (segments) {
    return (
      <div
        ref={ref}
        className={`${className} ${className}-split`}
        style={{ transformOrigin: 'left center', color }}
        onClick={onClick}
      >
        {segments.map((segment, i) => (
          <span
            key={i}
            className={`${className}-call`}
            style={{ left: `${segment.left}%`, width: `${segment.width}%`, backgroundColor: color }}
          />
        ))}
      </div>
    );
  }

  if (dashed && !endAt) {
    return (
      <div
        ref={ref}
        className={`${className} ${className}-split ${className}-pulse`}
        style={{ color }}
        onClick={onClick}
      />
    );
  }

  return (
    <div
      ref={ref}
      className={`${className}${stateClass}`}
      style={{ transformOrigin: 'left center', ...(lineColor ? { backgroundColor: lineColor } : {}) }}
      onClick={onClick}
    />
  );
};
