import React, { useEffect, useState } from 'react';
import type { SlotBidArtifactsResponse } from '../types';
import { downloadSSZ } from './Popover';

interface BidArtifactLinksProps {
  slot: number;
  transport: string; // "p2p" | "builder-api"
  valueGwei: number;
  timeMs: number; // event timestamp (unix ms) to disambiguate equal values
}

// BidArtifactLinks resolves the stored SSZ artifact behind a bid dot: the
// graph state (SSE-driven) does not know artifact indices, so the slot's bid
// artifact listing is fetched on popover open and matched by transport +
// value + nearest timestamp.
export const BidArtifactLinks: React.FC<BidArtifactLinksProps> = ({ slot, transport, valueGwei, timeMs }) => {
  const [state, setState] = useState<'loading' | 'none' | 'error'>('loading');
  const [index, setIndex] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;

    fetch(`/api/buildoor/slot-results/${slot}/bids`)
      .then(async res => {
        if (res.status === 404) throw new Error('none');
        if (!res.ok) throw new Error(`request failed (${res.status})`);
        return res.json() as Promise<SlotBidArtifactsResponse>;
      })
      .then(data => {
        if (cancelled) return;
        const candidates = (data.bids || []).filter(
          b => (b.transport ?? '') === transport && (b.total_value_gwei ?? -1) === valueGwei
        );
        if (candidates.length === 0) {
          setState('none');
          return;
        }
        // Closest stored-at timestamp wins when several bids share the value.
        candidates.sort((a, b) =>
          Math.abs((a.at ?? 0) - timeMs) - Math.abs((b.at ?? 0) - timeMs));
        setIndex(candidates[0].index);
        setState('none'); // state unused once index is set
      })
      .catch(() => { if (!cancelled) setState('error'); });

    return () => { cancelled = true; };
  }, [slot, transport, valueGwei, timeMs]);

  if (index === null) {
    return state === 'loading'
      ? <div className="popover-artifacts text-muted small">resolving artifact…</div>
      : null;
  }

  const url = `/api/buildoor/slot-results/${slot}/bids/${index}`;
  return (
    <div className="popover-artifacts d-flex gap-1">
      <a href={url} target="_blank" rel="noreferrer" className="btn btn-outline-secondary ap-artifact-btn">
        JSON
      </a>
      <button
        type="button"
        className="btn btn-outline-secondary ap-artifact-btn"
        onClick={() => {
          downloadSSZ(url, `slot-${slot}-bid-${index}.ssz`)
            .catch((err) => console.error('artifact download failed:', err));
        }}
      >
        SSZ
      </button>
    </div>
  );
};
