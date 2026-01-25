import React, { useRef, useEffect } from 'react';

interface CurrentTimeIndicatorProps {
  slotStartTime: number;
  rangeStart: number;
  totalRange: number;
}

export const CurrentTimeIndicator: React.FC<CurrentTimeIndicatorProps> = ({
  slotStartTime,
  rangeStart,
  totalRange
}) => {
  const indicatorRef = useRef<HTMLDivElement>(null);
  const animationRef = useRef<number>(0);

  useEffect(() => {
    const updatePosition = () => {
      if (!indicatorRef.current) return;

      const now = Date.now();
      const currentTimeRelativeMs = now - slotStartTime;
      const position = ((currentTimeRelativeMs - rangeStart) / totalRange) * 100;

      if (position >= 0 && position <= 100) {
        indicatorRef.current.style.left = `${position}%`;
        indicatorRef.current.style.display = 'block';
      } else {
        indicatorRef.current.style.display = 'none';
      }

      animationRef.current = requestAnimationFrame(updatePosition);
    };

    animationRef.current = requestAnimationFrame(updatePosition);

    return () => {
      if (animationRef.current) {
        cancelAnimationFrame(animationRef.current);
      }
    };
  }, [slotStartTime, rangeStart, totalRange]);

  return <div ref={indicatorRef} className="current-time-indicator" />;
};
