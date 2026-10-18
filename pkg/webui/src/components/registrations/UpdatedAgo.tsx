import React, { useEffect, useState } from 'react';

function formatAge(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  return `${Math.floor(m / 60)}h ago`;
}

function formatInterval(ms: number): string {
  const s = Math.round(ms / 1000);
  return s % 60 === 0 ? `${s / 60} min` : `${s} s`;
}

interface UpdatedAgoProps {
  updatedAt: number | null;
  intervalMs: number;
}

// UpdatedAgo shows the age of the last successful fetch and the poll cadence.
export const UpdatedAgo: React.FC<UpdatedAgoProps> = ({ updatedAt, intervalMs }) => {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);

  return (
    <span className="bd-faint small text-nowrap">
      {updatedAt === null ? 'not loaded yet' : `updated ${formatAge(now - updatedAt)}`}
      {' · '}refreshes every {formatInterval(intervalMs)}
    </span>
  );
};
