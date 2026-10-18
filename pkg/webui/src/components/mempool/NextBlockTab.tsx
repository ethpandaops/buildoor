import React from 'react';
import { StatTile } from '../ui/StatTile';
import { Tag } from '../ui/Tag';
import { ErrorNotice, LockNotice } from './LockNotice';
import { weiToGwei } from './format';
import type { TxPoolPreviewResponse } from '../../types';

const SkipTags: React.FC<{ skipped?: Record<string, number> }> = ({ skipped }) => {
  const entries = Object.entries(skipped ?? {}).filter(([, n]) => n > 0);
  if (entries.length === 0) return <span className="bd-muted small">nothing skipped</span>;
  return (
    <span className="d-inline-flex flex-wrap gap-1">
      {entries.map(([reason, n]) => (
        <Tag key={reason} title={reason}>{reason}: {n}</Tag>
      ))}
    </span>
  );
};

interface NextBlockTabProps {
  preview: TxPoolPreviewResponse | null;
  error: string | null;
  locked: boolean;
  onRefresh: () => void;
}

// NextBlockTab shows the dry-run selection on the current head with the
// live settings: what the testing builder would take from the pool right now.
export const NextBlockTab: React.FC<NextBlockTabProps> = ({ preview, error, locked, onRefresh }) => {
  if (locked) {
    return <LockNotice>The pool holds private transactions: log in to see the next block preview.</LockNotice>;
  }
  const selection = preview?.selection ?? null;
  const gasLimit = preview?.gas_limit ?? 0;

  return (
    <div className="card">
      <div className="card-body">
        <div className="bd-mp-toolbar mb-3">
          <div>
            <h3 className="bd-card-title">Next block preview</h3>
            <div className="bd-muted small">dry-run selection on the current head with the live settings</div>
          </div>
          <button type="button" className="bd-btn bd-btn-sm" onClick={onRefresh}>
            <i className="fas fa-rotate"></i>Refresh
          </button>
        </div>
        <ErrorNotice message={error} />
        {selection ? (
          <>
            <div className="bd-mp-preview-grid">
              <StatTile label="Selected" value={`${selection.selected} / ${selection.pool_size}`} note="transactions" />
              <StatTile
                label="Gas sum"
                value={selection.gas_sum.toLocaleString()}
                note={
                  <>
                    of {selection.gas_budget.toLocaleString()} budget
                    {gasLimit ? ` · limit ${gasLimit.toLocaleString()}` : ''}
                  </>
                }
              />
              <StatTile label="Base fee" value={selection.base_fee ? `${weiToGwei(selection.base_fee)} gwei` : '—'} />
              <StatTile label="Blobs" value={selection.blobs ?? 0} />
            </div>
            {selection.ceiling_applied && (
              <div className="mt-2">
                <Tag tone="warn" title="Next base fee above txpool.base_fee_ceiling_gwei: fill reduced to the gas target">
                  base fee ceiling: fill reduced to the gas target
                </Tag>
              </div>
            )}
            <div className="mt-3 d-flex flex-wrap align-items-center gap-2 small">
              <span className="bd-muted">Skipped</span>
              <SkipTags skipped={selection.skipped} />
            </div>
            {preview?.parent_hash && (
              <div className="mt-2 bd-muted small font-monospace text-break">parent {preview.parent_hash}</div>
            )}
          </>
        ) : (
          !error && <div className="bd-muted small">No preview yet.</div>
        )}
      </div>
    </div>
  );
};
