import React from 'react';
import { Pagination } from '../Pagination';
import { StatTile } from '../ui/StatTile';
import { Tag } from '../ui/Tag';
import { ErrorNotice } from './LockNotice';
import { FeeChart } from './FeeChart';
import { relativeTime, weiToGwei } from './format';
import type { ELMempoolResponse, FeeDistribution, TxPoolTx } from '../../types';

const TX_TYPES: Record<number, string> = { 0: 'legacy', 1: 'access-list', 2: 'eip-1559', 3: 'blob', 4: 'set-code' };

const MODE_LABELS: Record<string, { label: string; tone: 'ok' | 'info' | 'warn'; title: string }> = {
  websocket: { label: 'live (websocket)', tone: 'ok', title: 'Arrivals stream in over --el-ws' },
  filter: { label: 'polling (filter)', tone: 'info', title: 'A pending transaction filter is polled every 500 ms' },
  resync: { label: 'resync only', tone: 'warn', title: 'The EL offers no filter: full reads every 5 minutes only' },
};

interface ELMempoolTabProps {
  data: ELMempoolResponse | null;
  fees: FeeDistribution | null;
  error: string | null;
  loading: boolean;
  isLoggedIn: boolean;
  pageLimit: number;
  offset: number;
  onOffsetChange: (offset: number) => void;
  sender: string;
  onSenderChange: (sender: string) => void;
  sort: string;
  onSortChange: (sort: string) => void;
}

// ELMempoolTab shows buildoor's mirror of the EL mempool (the testing build's
// fill source, public transactions): its state, the pending gas by fee with
// buildoor's private transactions highlighted (logged in), and its content.
export const ELMempoolTab: React.FC<ELMempoolTabProps> = ({
  data,
  fees,
  error,
  loading,
  isLoggedIn,
  pageLimit,
  offset,
  onOffsetChange,
  sender,
  onSenderChange,
  sort,
  onSortChange,
}) => {
  const stats = data?.stats;
  const mode = stats?.mode ? MODE_LABELS[stats.mode] : undefined;
  const txs = data?.txs ?? [];

  return (
    <div className="d-flex flex-column gap-3">
      <ErrorNotice message={error} />

      <div className="card">
        <div className="card-body">
          <div className="bd-mp-toolbar mb-3">
            <div>
              <h3 className="bd-card-title">
                EL mempool mirror{' '}
                {stats && !stats.active && <Tag title="Starts with the first testing build that fills from the EL mempool">idle</Tag>}
                {mode && <Tag tone={mode.tone} title={mode.title}>{mode.label}</Tag>}
              </h3>
              <div className="bd-muted small">
                the EL&apos;s pending transactions, kept in sync for the testing build&apos;s EL mempool fill
              </div>
            </div>
          </div>
          <div className="bd-mp-preview-grid">
            <StatTile label="Transactions" value={(stats?.txs ?? 0).toLocaleString()} note={`${(stats?.senders ?? 0).toLocaleString()} senders`} />
            <StatTile label="Gas" value={(stats?.gas_sum ?? 0).toLocaleString()} note="sum of gas limits" />
            <StatTile label="Blob txs" value={(stats?.blob_txs ?? 0).toLocaleString()} />
            <StatTile
              label="Last resync"
              value={stats?.synced_at ? relativeTime(stats.synced_at) : '—'}
              note={stats?.started ? `running since ${relativeTime(stats.started)}` : undefined}
            />
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-body">
          <h3 className="bd-card-title mb-1">Pending gas by fee</h3>
          <div className="bd-muted small mb-2">
            max fee per gas against the next base fee
            {isLoggedIn ? ', buildoor’s private transactions highlighted' : ' (log in to include buildoor’s private transactions)'}
            {fees && !fees.mirror_active ? ' · the EL mirror is idle, its part may be stale' : ''}
          </div>
          {fees ? <FeeChart fees={fees} /> : !error && <div className="bd-muted small">Loading…</div>}
        </div>
      </div>

      <div className="card">
        <div className="card-body bd-mp-toolbar">
          <h3 className="bd-card-title">
            Pending transactions <Tag tone="accent">{(data?.total ?? 0).toLocaleString()}</Tag>
          </h3>
          <div className="bd-mp-toolbar-controls">
            <input
              type="text"
              className="form-control form-control-sm font-monospace bd-mp-input-sender"
              placeholder="filter by sender 0x…"
              aria-label="Filter by sender"
              value={sender}
              onChange={(e) => { onSenderChange(e.target.value); onOffsetChange(0); }}
            />
            <select
              className="form-select form-select-sm w-auto"
              aria-label="Sort order"
              value={sort}
              onChange={(e) => onSortChange(e.target.value)}
            >
              <option value="arrival">arrival</option>
              <option value="sender">sender / nonce</option>
              <option value="tip">tip</option>
            </select>
          </div>
        </div>
        <div className="position-relative">
          {loading && !data ? (
            <div className="text-center py-5"><div className="spinner-border" role="status"><span className="visually-hidden">Loading...</span></div></div>
          ) : txs.length === 0 ? (
            <div className="text-center py-5 bd-muted">{stats?.active ? 'No pending transactions' : 'The mirror is idle'}</div>
          ) : (
            <div className="table-responsive">
              <table className="table table-sm table-hover mb-0 small align-middle">
                <thead>
                  <tr>
                    <th>Hash</th>
                    <th>Sender</th>
                    <th className="text-end">Nonce</th>
                    <th>Type</th>
                    <th className="text-end">Gas</th>
                    <th className="text-end">Max fee / tip (gwei)</th>
                    <th className="text-end">Blobs</th>
                    <th>Seen</th>
                  </tr>
                </thead>
                <tbody>
                  {txs.map((tx: TxPoolTx) => (
                    <tr key={tx.hash}>
                      <td className="font-monospace" title={tx.hash}>{tx.hash.slice(0, 10)}…{tx.hash.slice(-6)}</td>
                      <td className="font-monospace" title={tx.sender}>
                        <a href="#" onClick={(e) => { e.preventDefault(); onSenderChange(tx.sender); onOffsetChange(0); }}>
                          {tx.sender.slice(0, 8)}…{tx.sender.slice(-4)}
                        </a>
                      </td>
                      <td className="text-end">{tx.nonce}</td>
                      <td>{TX_TYPES[tx.type] ?? tx.type}</td>
                      <td className="text-end">{tx.gas.toLocaleString()}</td>
                      <td className="text-end">{weiToGwei(tx.max_fee_per_gas)} / {weiToGwei(tx.max_priority_fee_per_gas)}</td>
                      <td className="text-end">{tx.blobs ?? 0}</td>
                      <td title={tx.arrived}>{relativeTime(tx.arrived)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
        {data && data.total > pageLimit && (
          <div className="card-footer">
            <Pagination total={data.total} offset={offset} limit={pageLimit} onPageChange={onOffsetChange} />
          </div>
        )}
      </div>
    </div>
  );
};
