import React, { useEffect, useState } from 'react';
import type { HeadVoteDetail } from '../types';

interface HeadVoteHeatmapProps {
  slot: number;
  slotDurationMs: number;
}

// Per-name vote-arrival heatmap rendered inside the Head Vote Participation
// popover: rows = validator-ranges client names, columns = time buckets from
// the slot start, cell intensity = vote arrivals in that bucket. A separate
// trailing column counts attesters that landed on chain without ever being
// seen as raw singles. Data is fetched on mount from the head-votes REST
// endpoint (only slots still retained by the tracker are served).
export const HeadVoteHeatmap: React.FC<HeadVoteHeatmapProps> = ({ slot, slotDurationMs }) => {
  const [detail, setDetail] = useState<HeadVoteDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    fetch(`/api/buildoor/head-votes/${slot}`)
      .then(async res => {
        if (res.status === 404) throw new Error('No vote detail retained for this slot');
        if (!res.ok) throw new Error(`Request failed (${res.status})`);
        return res.json() as Promise<HeadVoteDetail>;
      })
      .then(data => { if (!cancelled) setDetail(data); })
      .catch(err => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)); });

    return () => { cancelled = true; };
  }, [slot]);

  if (error) {
    return <div className="hv-heatmap hv-heatmap-note">{error}</div>;
  }
  if (!detail) {
    return <div className="hv-heatmap hv-heatmap-note">Loading vote arrivals…</div>;
  }
  if (detail.rows.length === 0) {
    return <div className="hv-heatmap hv-heatmap-note">No votes recorded for this slot</div>;
  }

  const { bucket_ms: bucketMs, bucket_count: bucketCount } = detail;
  // Bucket index at which the attestation deadline (25% of the slot) and the
  // slot end fall — rendered as vertical rules on the grid.
  const deadlineBucket = Math.round((slotDurationMs * 0.25) / bucketMs);
  const slotEndBucket = Math.round(slotDurationMs / bucketMs);

  const fmtSec = (ms: number): string => `${(ms / 1000).toFixed(2)}s`;

  const cellTitle = (count: number, bucket: number): string => {
    const from = bucket * bucketMs;
    const isLast = bucket === bucketCount - 1;
    return `${count} vote${count === 1 ? '' : 's'} @ ${fmtSec(from)}–${isLast ? '…' : fmtSec(from + bucketMs)}`;
  };

  return (
    <div className="hv-heatmap">
      <div className="hv-heatmap-title">Vote arrivals by node</div>
      {detail.rows.map(row => {
        const rowMax = Math.max(1, ...row.counts);
        return (
          <div className="hv-heatmap-row" key={row.name}>
            <div className="hv-heatmap-name" title={row.name}>
              <span className="hv-heatmap-name-text">{row.name}</span>
              <span className="hv-heatmap-count">{row.seen}/{row.members}</span>
            </div>
            <div className="hv-heatmap-cells">
              {row.counts.map((count, i) => (
                <div
                  key={i}
                  className={
                    'hv-heatmap-cell' +
                    (i === deadlineBucket ? ' hv-heatmap-cell-deadline' : '') +
                    (i === slotEndBucket ? ' hv-heatmap-cell-slot-end' : '')
                  }
                  style={count > 0
                    ? { background: `rgba(0, 188, 212, ${(0.2 + 0.8 * (count / rowMax)).toFixed(2)})` }
                    : undefined}
                  title={cellTitle(count, i)}
                />
              ))}
            </div>
            <div
              className={`hv-heatmap-missed ${row.in_block_unseen > 0 ? 'hv-heatmap-missed-some' : ''}`}
              title={`${row.in_block_unseen} attester${row.in_block_unseen === 1 ? '' : 's'} on chain but never seen as raw single`}
            >
              {row.in_block_unseen > 0 ? row.in_block_unseen : ''}
            </div>
          </div>
        );
      })}
      <div className="hv-heatmap-row hv-heatmap-axis-row">
        <div className="hv-heatmap-name" />
        <div className="hv-heatmap-axis">
          <span style={{ left: '0%' }}>0s</span>
          <span style={{ left: `${(deadlineBucket / bucketCount) * 100}%` }}>25%</span>
          <span style={{ left: `${(slotEndBucket / bucketCount) * 100}%` }}>slot end</span>
        </div>
        <div className="hv-heatmap-missed" title="On chain but never seen as raw single">∅</div>
      </div>
    </div>
  );
};
