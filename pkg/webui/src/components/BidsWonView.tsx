import React, { useState, useEffect, useRef } from 'react';
import { BidsWonTable } from './BidsWonTable';
import { useBidsWon } from '../hooks/useBidsWon';
import { onStreamEvent } from '../hooks/useEventStream';

export const BidsWonView: React.FC = () => {
  const [offset, setOffset] = useState(0);
  const limit = 50;
  const { bidsWon, total, loading, refetch } = useBidsWon(offset, limit);

  // Refs so the stream subscription below stays stable across renders while
  // still seeing the latest offset/refetch.
  const offsetRef = useRef(offset);
  const refetchRef = useRef(refetch);
  offsetRef.current = offset;
  refetchRef.current = refetch;

  // Auto-refresh the first page on bid_won events via the page's SHARED SSE
  // connection. A dedicated EventSource here would reconnect whenever its
  // effect deps changed and re-receive the connect-time replay burst each
  // time, feeding an endless refetch loop; the shared connection is stable
  // and its seq watermark already drops replayed duplicates on reconnect.
  useEffect(() => {
    return onStreamEvent('bid_won', () => {
      if (offsetRef.current === 0) {
        refetchRef.current();
      }
    });
  }, []);

  return (
    <div className="container-fluid mt-2">
      <BidsWonTable
        bidsWon={bidsWon}
        total={total}
        offset={offset}
        limit={limit}
        loading={loading}
        onPageChange={setOffset}
      />
    </div>
  );
};
