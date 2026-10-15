import React from 'react';
import { CopyButton, shortHash } from './CopyField';
import type { BidWonEntry } from '../../types';

interface BlocksWonCardProps {
  entries: BidWonEntry[];
  total: number | null;
  loaded: boolean;
  onShowAll: () => void;
}

function relativeTime(ts: number): string {
  const diff = Date.now() - ts;
  if (diff < 60_000) return 'just now';
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return `${Math.floor(diff / 86_400_000)}d ago`;
}

function formatValue(valueEth: string): string {
  const n = parseFloat(valueEth);
  if (!Number.isFinite(n)) return valueEth;
  return n.toLocaleString(undefined, { maximumFractionDigits: 5 });
}

export const BlocksWonCard: React.FC<BlocksWonCardProps> = ({ entries, total, loaded, onShowAll }) => (
  <section className="card" aria-labelledby="ov-wins-title">
    <div className="card-body">
      <div className="ov-section-head">
        <h2 id="ov-wins-title" className="bd-card-title">
          Blocks won
        </h2>
        {total !== null && total > 0 && (
          <a
            href="/bids-won"
            className="ov-link"
            onClick={(e) => {
              e.preventDefault();
              onShowAll();
            }}
          >
            All blocks ({total.toLocaleString()}) <i className="fas fa-arrow-right" aria-hidden="true" />
          </a>
        )}
      </div>
      {!loaded ? (
        <p className="bd-muted ov-small mb-0">Loading…</p>
      ) : entries.length === 0 ? (
        <p className="bd-muted ov-small mb-0">No block built by buildoor has been included on chain yet.</p>
      ) : (
        <div className="table-responsive">
          <table className="table table-sm align-middle mb-0 ov-table">
            <thead>
              <tr>
                <th scope="col">Slot</th>
                <th scope="col">Block hash</th>
                <th scope="col" className="text-end">
                  Txs
                </th>
                <th scope="col" className="text-end">
                  Blobs
                </th>
                <th scope="col" className="text-end">
                  Value (ETH)
                </th>
                <th scope="col" className="text-end">
                  When
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <tr key={`${entry.slot}-${entry.block_hash}`}>
                  <th scope="row" className="fw-medium">
                    {entry.slot.toLocaleString()}
                  </th>
                  <td>
                    <span className="ov-hash">
                      <code title={entry.block_hash}>{shortHash(entry.block_hash)}</code>
                      <CopyButton value={entry.block_hash} label={`block hash of slot ${entry.slot}`} />
                    </span>
                  </td>
                  <td className="text-end">{entry.num_transactions}</td>
                  <td className="text-end">{entry.num_blobs}</td>
                  <td className="text-end ov-num">{formatValue(entry.value_eth)}</td>
                  <td className="text-end bd-muted">
                    <time dateTime={new Date(entry.timestamp).toISOString()} title={new Date(entry.timestamp).toLocaleString()}>
                      {relativeTime(entry.timestamp)}
                    </time>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  </section>
);
