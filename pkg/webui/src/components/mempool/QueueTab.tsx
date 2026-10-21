import React, { useState } from 'react';
import { Pagination } from '../Pagination';
import { Tag } from '../ui/Tag';
import { ErrorNotice, LockNotice } from './LockNotice';
import { formatBytes, relativeTime, weiToEth, weiToGwei } from './format';
import type { TxPoolResponse, TxPoolTx } from '../../types';

const TX_TYPES: Record<number, string> = { 0: 'legacy', 1: 'access-list', 2: 'eip-1559', 3: 'blob', 4: 'set-code' };

// SendHint explains what the private mempool is for and how to feed it.
const SendHint: React.FC<{ ingressUrl: string }> = ({ ingressUrl }) => (
  <details className="bd-mp-howto">
    <summary>How to send transactions</summary>
    <div className="bd-mp-howto-body">
      <p>
        buildoor&apos;s own mempool, separate from the EL&apos;s. Transactions sent here never enter
        the EL mempool: only the <strong>testing builder</strong> (transactions from the buildoor mempool)
        includes them, and they land on chain only when buildoor bids with the custom block and wins.
        The regular engine build always uses the EL mempool. Ingress (JSON-RPC,{' '}
        <code>eth_sendRawTransaction</code> + read passthrough): <code>{ingressUrl}</code>
      </p>
      <p className="mb-1">With spamoor, make buildoor the <strong>only</strong> host — a second host would leak the transactions into the EL mempool:</p>
      <pre className="bd-mp-pre">{`spamoor <scenario> -h "name(buildoor)${ingressUrl}" -p <privkey> ...`}</pre>
    </div>
  </details>
);

interface QueueTabProps {
  ingressUrl: string;
  data: TxPoolResponse | null;
  loading: boolean;
  error: string | null;
  unauthorized: boolean;
  isLoggedIn: boolean;
  pageLimit: number;
  offset: number;
  onOffsetChange: (offset: number) => void;
  sender: string;
  onSenderChange: (sender: string) => void;
  sort: string;
  onSortChange: (sort: string) => void;
  onDrop: (hash: string) => Promise<string | null>;
}

// QueueTab lists the queued transactions (operators only) with a sender
// filter, sort order, pagination and per-transaction drop.
export const QueueTab: React.FC<QueueTabProps> = ({
  ingressUrl,
  data,
  loading,
  error,
  unauthorized,
  isLoggedIn,
  pageLimit,
  offset,
  onOffsetChange,
  sender,
  onSenderChange,
  sort,
  onSortChange,
  onDrop,
}) => {
  const [dropping, setDropping] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);

  const handleDrop = async (hash: string) => {
    setDropping(hash);
    setMessage(null);
    const err = await onDrop(hash);
    setDropping(null);
    if (err) setMessage(err);
  };

  const locked = !isLoggedIn || unauthorized;
  const txs = data?.txs ?? [];

  return (
    <div className="d-flex flex-column gap-3">
      <SendHint ingressUrl={ingressUrl} />
      {locked ? (
        <LockNotice>The pool holds private transactions: log in to view the queued transactions.</LockNotice>
      ) : (
        <div className="card">
          <div className="card-body bd-mp-toolbar">
            <h3 className="bd-card-title">
              Private transactions <Tag tone="accent">{(data?.total ?? 0).toLocaleString()}</Tag>
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
          <ErrorNotice message={error} />
          <ErrorNotice message={message} onDismiss={() => setMessage(null)} />
          <div className="position-relative">
            {loading && (
              <div className="bd-mp-loading">
                <div className="spinner-border" role="status"><span className="visually-hidden">Loading...</span></div>
              </div>
            )}
            {!loading && txs.length === 0 ? (
              <div className="text-center py-5 bd-muted">No queued transactions</div>
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
                      <th className="text-end">Value (ETH)</th>
                      <th className="text-end">Blobs</th>
                      <th className="text-end">Size</th>
                      <th className="text-end" title="Attributed EL refusals">Strikes</th>
                      <th>Arrived</th>
                      <th></th>
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
                        <td className="text-end">{weiToGwei(tx.max_fee_per_gas)} / {weiToGwei(tx.max_priority_fee_per_gas)}{tx.cold && <> <Tag title="Far below the base fee: the body waits outside memory until the base fee comes close">cold</Tag></>}</td>
                        <td className="text-end">{weiToEth(tx.value_wei)}</td>
                        <td className="text-end">{tx.blobs ?? 0}</td>
                        <td className="text-end">{formatBytes(tx.size)}</td>
                        <td className="text-end">{tx.strikes ? <Tag tone="warn">{tx.strikes}</Tag> : ''}</td>
                        <td title={`${tx.arrived} (slot ${tx.arrived_slot})`}>
                          {relativeTime(tx.arrived)} <span className="bd-muted">s{tx.arrived_slot}</span>
                        </td>
                        <td className="text-end">
                          <button
                            type="button"
                            className="bd-btn bd-btn-sm bd-btn-danger bd-mp-icon-btn"
                            disabled={dropping !== null}
                            title="Drop"
                            aria-label="Drop transaction"
                            onClick={() => handleDrop(tx.hash)}
                          >
                            <i className="fas fa-times"></i>
                          </button>
                        </td>
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
      )}
    </div>
  );
};
