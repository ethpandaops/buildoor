import React, { useState, useEffect } from 'react';
import { BidsWonTable } from './BidsWonTable';
import { useBidsWon } from '../hooks/useBidsWon';

export const BidsWonView: React.FC = () => {
  const [offset, setOffset] = useState(0);
  const limit = 50;
  const { bidsWon, total, loading, refetch } = useBidsWon(offset, limit);

  // Listen for real-time bid_won events
  useEffect(() => {
    const eventSource = new EventSource('/api/events');

    eventSource.addEventListener('message', (e) => {
      try {
        const event = JSON.parse(e.data);
        if (event.type === 'bid_won') {
          // Only auto-refresh if on first page
          if (offset === 0) {
            refetch();
          }
        }
      } catch (err) {
        console.error('Error parsing event:', err);
      }
    });

    return () => {
      eventSource.close();
    };
  }, [offset, refetch]);

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
