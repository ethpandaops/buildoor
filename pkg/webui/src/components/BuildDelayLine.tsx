import React, { useRef, useEffect } from 'react';

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
}

// BuildDelayLine renders the payload build span on the slot timeline.
// While the build is in progress (endAt unset) it animates its right edge toward
// the current time via requestAnimationFrame, so the build is visible as it
// progresses. Once the build finalizes (payload ready) it renders a static line
// from build start to the end time. Build failures are rendered as a red dot by
// the parent instead — a thin line is too hard to see and click.
export const BuildDelayLine: React.FC<BuildDelayLineProps> = ({
  leftPct,
  slotStartTime,
  rangeStart,
  totalRange,
  endAt,
  expectedEndAt,
  onClick,
  className = 'build-delay-line'
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const animationRef = useRef<number>(0);
  const left = Math.max(0, leftPct);

  useEffect(() => {
    const apply = (rightPct: number) => {
      if (!ref.current) return;
      const right = Math.min(100, rightPct);
      if (right > left) {
        ref.current.style.left = `${left}%`;
        ref.current.style.width = `${right - left}%`;
        ref.current.style.display = 'block';
      } else {
        ref.current.style.display = 'none';
      }
    };

    // Finalized build: static line from build start to the end (ready or failed) time.
    if (endAt) {
      apply(((endAt - slotStartTime - rangeStart) / totalRange) * 100);
      return;
    }

    // In-progress build: grow the right edge toward "now", capped at the expected
    // completion time so a stuck/timed-out build holds a bounded bar.
    const update = () => {
      const now = Date.now();
      const edge = expectedEndAt !== undefined ? Math.min(now, expectedEndAt) : now;
      apply(((edge - slotStartTime - rangeStart) / totalRange) * 100);

      // Once we've reached the cap, hold position and stop animating.
      if (expectedEndAt !== undefined && now >= expectedEndAt) {
        return;
      }
      animationRef.current = requestAnimationFrame(update);
    };
    animationRef.current = requestAnimationFrame(update);

    return () => {
      if (animationRef.current) {
        cancelAnimationFrame(animationRef.current);
      }
    };
  }, [left, slotStartTime, rangeStart, totalRange, endAt, expectedEndAt]);

  // Styling: finalized (plain) vs in-progress (pulsing).
  const stateClass = endAt ? '' : ` ${className}-active`;

  return (
    <div
      ref={ref}
      className={`${className}${stateClass}`}
      onClick={onClick}
    />
  );
};
