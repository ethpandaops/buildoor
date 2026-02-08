import React, { useState } from 'react';
import { BidWonEntry } from '../types';
import { Pagination } from './Pagination';

interface BidsWonTableProps {
  bidsWon: BidWonEntry[];
  total: number;
  offset: number;
  limit: number;
  loading: boolean;
  onPageChange: (offset: number) => void;
}

export const BidsWonTable: React.FC<BidsWonTableProps> = ({
  bidsWon,
  total,
  offset,
  limit,
  loading,
  onPageChange,
}) => {
  const [copiedHash, setCopiedHash] = useState<string | null>(null);

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
    setCopiedHash(text);
    setTimeout(() => setCopiedHash(null), 2000);
  };

  const truncateHash = (hash: string) => {
    if (hash.length <= 16) return hash;
    return `${hash.slice(0, 10)}...${hash.slice(-6)}`;
  };

  const formatTimestamp = (ts: number) => {
    const date = new Date(ts);
    const now = Date.now();
    const diff = now - ts;

    if (diff < 60000) return 'Just now';
    if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
    if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`;
    return date.toLocaleString();
  };

  return (
    <div className="card">
      <div className="card-header">
        <h5 className="mb-0">
          Bids Won <span className="badge bg-primary ms-2">{total}</span>
        </h5>
      </div>
      <div className="card-body p-0">
        <div style={{ maxHeight: '600px', overflowY: 'auto', position: 'relative' }}>
          {loading && (
            <div className="position-absolute top-0 start-0 w-100 h-100 d-flex align-items-center justify-content-center bg-white bg-opacity-75" style={{ zIndex: 10 }}>
              <div className="spinner-border text-primary" role="status">
                <span className="visually-hidden">Loading...</span>
              </div>
            </div>
          )}

          {!loading && bidsWon.length === 0 ? (
            <div className="text-center py-5 text-muted">
              No bids won yet
            </div>
          ) : (
            <table className="table table-sm table-hover mb-0">
              <thead className="table-light sticky-top">
                <tr>
                  <th>Slot</th>
                  <th>Block Hash</th>
                  <th className="text-end">Txs</th>
                  <th className="text-end">Blobs</th>
                  <th className="text-end">Value (ETH)</th>
                  <th className="text-end">Time</th>
                </tr>
              </thead>
              <tbody>
                {bidsWon.map((bid) => (
                  <tr key={`${bid.slot}-${bid.block_hash}`}>
                    <td className="font-monospace">{bid.slot}</td>
                    <td>
                      <button
                        className="btn btn-link btn-sm p-0 font-monospace text-decoration-none"
                        onClick={() => copyToClipboard(bid.block_hash)}
                        title={bid.block_hash}
                      >
                        {truncateHash(bid.block_hash)}
                        {copiedHash === bid.block_hash && (
                          <small className="text-success ms-1">✓</small>
                        )}
                      </button>
                    </td>
                    <td className="text-end">{bid.num_transactions}</td>
                    <td className="text-end">{bid.num_blobs}</td>
                    <td className="text-end font-monospace">
                      {parseFloat(bid.value_eth).toFixed(6)}
                    </td>
                    <td className="text-end text-muted">
                      <small>{formatTimestamp(bid.timestamp)}</small>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <Pagination
          total={total}
          offset={offset}
          limit={limit}
          onPageChange={onPageChange}
        />
      </div>
    </div>
  );
};
