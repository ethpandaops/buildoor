import React, { useEffect, useMemo, useState } from 'react';
import { useEventStream } from '../hooks/useEventStream';
import { useTxPool } from '../hooks/useTxPool';
import { useSettings } from '../hooks/useSettings';
import { useLocalBuildStatus } from '../hooks/useLocalBuildStatus';
import { Pagination } from './Pagination';
import type { TxPoolStats, TxPoolTx, TxSelectionSummary } from '../types';

const PAGE_LIMIT = 50;

function weiToEth(wei: string | undefined): string {
  if (!wei) return '0';
  try {
    const value = BigInt(wei);
    const whole = value / 1000000000000000000n;
    const frac = (value % 1000000000000000000n).toString().padStart(18, '0').slice(0, 6);
    return `${whole}.${frac}`;
  } catch {
    return wei;
  }
}

function weiToGwei(wei: string | undefined): string {
  if (!wei) return '0';
  try {
    const value = Number(BigInt(wei)) / 1e9;
    return value >= 100 ? value.toFixed(0) : value.toFixed(3);
  } catch {
    return wei;
  }
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(2)} MiB`;
}

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  if (diff < 1000) return 'just now';
  if (diff < 60000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
  return `${Math.floor(diff / 3600000)}h ago`;
}

const TX_TYPES: Record<number, string> = { 0: 'legacy', 1: 'access-list', 2: 'eip-1559', 3: 'blob', 4: 'set-code' };

const StatCard: React.FC<{ label: string; value: React.ReactNode; hint?: string }> = ({ label, value, hint }) => (
  <div className="col-6 col-md-4 col-xl-2">
    <div className="config-item h-100" title={hint}>
      <div className="config-item-label">{label}</div>
      <div className="config-item-value fs-5">{value}</div>
    </div>
  </div>
);

const SkipChips: React.FC<{ skipped?: Record<string, number> }> = ({ skipped }) => {
  const entries = Object.entries(skipped ?? {}).filter(([, n]) => n > 0);
  if (entries.length === 0) return <span className="text-muted small">nothing skipped</span>;
  return (
    <span className="d-inline-flex flex-wrap gap-1">
      {entries.map(([reason, n]) => (
        <span key={reason} className="badge bg-secondary" title={reason}>
          {reason}: {n}
        </span>
      ))}
    </span>
  );
};

const PreviewPanel: React.FC<{
  selection: TxSelectionSummary | null;
  parentHash?: string;
  gasLimit?: number;
  error: string | null;
  onRefresh: () => void;
}> = ({ selection, parentHash, gasLimit, error, onRefresh }) => (
  <div className="card mb-3">
    <div className="card-header py-1 d-flex align-items-center gap-2">
      <strong className="small">Next block preview</strong>
      <span className="text-muted small">dry-run selection on the current head with the live settings</span>
      <button className="btn btn-sm btn-outline-secondary ms-auto" onClick={onRefresh}>
        <i className="fas fa-rotate me-1"></i>Refresh
      </button>
    </div>
    <div className="card-body py-2">
      {error && <div className="alert alert-warning small py-1 px-2 mb-2">{error}</div>}
      {selection ? (
        <div className="row g-2">
          <div className="col-6 col-md-3">
            <div className="config-item">
              <div className="config-item-label">Selected</div>
              <div className="config-item-value">{selection.selected} / {selection.pool_size} txs</div>
            </div>
          </div>
          <div className="col-6 col-md-3">
            <div className="config-item">
              <div className="config-item-label">Gas sum</div>
              <div className="config-item-value">
                {selection.gas_sum.toLocaleString()} / {selection.gas_budget.toLocaleString()}
                {gasLimit ? <span className="text-muted small"> (limit {gasLimit.toLocaleString()})</span> : null}
              </div>
            </div>
          </div>
          <div className="col-6 col-md-3">
            <div className="config-item">
              <div className="config-item-label">Base fee</div>
              <div className="config-item-value">{selection.base_fee ? `${weiToGwei(selection.base_fee)} gwei` : '—'}</div>
            </div>
          </div>
          <div className="col-6 col-md-3">
            <div className="config-item">
              <div className="config-item-label">Blobs</div>
              <div className="config-item-value">{selection.blobs ?? 0}</div>
            </div>
          </div>
          <div className="col-12">
            <div className="config-item">
              <div className="config-item-label">Skipped</div>
              <div className="config-item-value"><SkipChips skipped={selection.skipped} /></div>
            </div>
          </div>
          {parentHash && (
            <div className="col-12 text-muted small font-monospace">parent {parentHash}</div>
          )}
        </div>
      ) : (
        !error && <div className="text-muted small">No preview yet.</div>
      )}
    </div>
  </div>
);

// MempoolView is the owned transaction pool page: enable toggle + ingress
// URL, content aggregates (pending, senders, gas sum, value, bytes, blobs),
// the next-block selection preview, and the paginated content table with
// per-transaction and clear-all actions.
export const MempoolView: React.FC = () => {
  const { txPoolStats, serviceStatus, chainInfo } = useEventStream();
  const { isLoggedIn, postSettings } = useSettings();
  const { status: localStatus } = useLocalBuildStatus(serviceStatus?.txpool_enabled);

  const [offset, setOffset] = useState(0);
  const [sender, setSender] = useState('');
  const [sort, setSort] = useState('arrival');
  const [message, setMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const query = useMemo(() => ({ offset, limit: PAGE_LIMIT, sender: sender.trim(), sort }), [offset, sender, sort]);
  const { data, loading, error, preview, previewError, fetchPreview, clear, drop } =
    useTxPool(query, txPoolStats?.version);

  useEffect(() => {
    fetchPreview();
    const interval = setInterval(fetchPreview, 10000);
    return () => clearInterval(interval);
  }, [fetchPreview, txPoolStats?.version]);

  const stats: TxPoolStats | null = txPoolStats ?? data?.stats ?? null;
  const enabled = stats?.enabled ?? serviceStatus?.txpool_enabled ?? false;
  const available = serviceStatus?.txpool_available ?? localStatus?.txpool.available ?? false;
  const ingressUrl = `${window.location.origin}/rpc`;

  const gasLimit = preview?.gas_limit ?? 0;
  const blocksToDrain = stats && gasLimit > 0 ? (stats.gas_sum / gasLimit).toFixed(2) : '—';

  const toggle = async (next: boolean) => {
    setBusy(true);
    setMessage(null);
    const err = await postSettings({ 'txpool.enabled': next });
    setBusy(false);
    if (err) setMessage(err);
  };

  const handleClear = async () => {
    if (!window.confirm('Drop every queued transaction?')) return;
    setBusy(true);
    const err = await clear();
    setBusy(false);
    if (err) setMessage(err);
  };

  const handleDrop = async (hash: string) => {
    setBusy(true);
    const err = await drop(hash);
    setBusy(false);
    if (err) setMessage(err);
  };

  const spamoorHint = `spamoor <scenario> -h "name(buildoor)${ingressUrl}" -p <privkey> ...   (buildoor as the ONLY host — a second host would leak the txs into the EL mempool)`;

  return (
    <div className="container-fluid px-0">
      <div className="card mb-3">
        <div className="card-header d-flex flex-wrap align-items-center gap-2">
          <h5 className="mb-0">Mempool</h5>
          <span className={`badge ${enabled ? 'bg-success' : 'bg-secondary'}`}>
            {enabled ? 'Enabled' : 'Disabled'}
          </span>
          {!available && (
            <span className="badge bg-warning text-dark" title={localStatus?.txpool.reason}>
              unavailable
            </span>
          )}
          <div className="ms-auto d-flex align-items-center gap-2">
            {isLoggedIn && (
              <div className="form-check form-switch mb-0">
                <input
                  className="form-check-input"
                  type="checkbox"
                  id="mempool-enabled"
                  checked={enabled}
                  disabled={busy || (!enabled && !available)}
                  onChange={(e) => toggle(e.target.checked)}
                />
                <label className="form-check-label small" htmlFor="mempool-enabled">accept transactions</label>
              </div>
            )}
            {isLoggedIn && (
              <button className="btn btn-sm btn-outline-danger" disabled={busy || !stats || stats.pending === 0} onClick={handleClear}>
                <i className="fas fa-trash me-1"></i>Clear
              </button>
            )}
          </div>
        </div>
        <div className="card-body py-2">
          {message && <div className="alert alert-warning small py-1 px-2 mb-2">{message}</div>}
          {error && <div className="alert alert-secondary small py-1 px-2 mb-2">{error}</div>}
          <div className="small text-muted mb-2">
            Transactions submitted here never enter the EL mempool: they are included only in payloads the
            local build assembles from this pool (payload source <code>local</code>), so they land on chain
            when buildoor's bid wins. Ingress (JSON-RPC, <code>eth_sendRawTransaction</code> + read passthrough):{' '}
            <code>{ingressUrl}</code>
          </div>
          <div className="small text-muted mb-3 font-monospace">{spamoorHint}</div>

          <div className="row g-2">
            <StatCard label="Pending txs" value={stats?.pending ?? 0} />
            <StatCard label="Senders" value={stats?.senders ?? 0} />
            <StatCard label="Gas sum" value={(stats?.gas_sum ?? 0).toLocaleString()} hint="Sum of the gas limits of all queued transactions" />
            <StatCard label="Blocks to drain" value={blocksToDrain} hint="Gas sum / current block gas limit" />
            <StatCard label="Value" value={`${weiToEth(stats?.value_wei)} ETH`} />
            <StatCard label="Size" value={formatBytes(stats?.bytes ?? 0)} />
            <StatCard label="Blob txs / blobs" value={`${stats?.blob_txs ?? 0} / ${stats?.blobs ?? 0}`} />
            <StatCard label="Admitted" value={stats?.admitted ?? 0} hint="Lifetime admissions" />
            <StatCard label="Included by us" value={stats?.evicted_included_by_us ?? 0} hint="Evicted because a block we built included them" />
            <StatCard label="Leaked" value={stats?.evicted_included_by_other ?? 0} hint="Included by a block we did not build (forward mode or duplicate submission elsewhere)" />
            <StatCard label="Nonce too low" value={stats?.evicted_nonce_too_low ?? 0} hint="Evicted because the chain passed their nonce" />
            <StatCard label="Expired" value={stats?.evicted_ttl ?? 0} hint="Evicted by the slot TTL" />
          </div>
          {stats && Object.keys(stats.rejected).length > 0 && (
            <div className="mt-2 small">
              <span className="text-muted me-1">Rejected:</span>
              <SkipChips skipped={stats.rejected} />
            </div>
          )}
        </div>
      </div>

      <PreviewPanel
        selection={preview?.selection ?? null}
        parentHash={preview?.parent_hash}
        gasLimit={preview?.gas_limit}
        error={previewError}
        onRefresh={fetchPreview}
      />

      <div className="card">
        <div className="card-header d-flex flex-wrap align-items-center gap-2">
          <h6 className="mb-0">Queued transactions <span className="badge bg-primary ms-1">{data?.total ?? 0}</span></h6>
          <div className="ms-auto d-flex gap-2">
            <input
              type="text"
              className="form-control form-control-sm font-monospace"
              style={{ width: '24rem' }}
              placeholder="filter by sender 0x…"
              value={sender}
              onChange={(e) => { setSender(e.target.value); setOffset(0); }}
            />
            <select className="form-select form-select-sm w-auto" value={sort} onChange={(e) => setSort(e.target.value)}>
              <option value="arrival">arrival</option>
              <option value="sender">sender / nonce</option>
              <option value="tip">tip</option>
            </select>
          </div>
        </div>
        <div className="card-body p-0 position-relative">
          {loading && (
            <div className="position-absolute top-0 start-0 w-100 h-100 d-flex align-items-center justify-content-center bg-body bg-opacity-75" style={{ zIndex: 10 }}>
              <div className="spinner-border text-primary" role="status"><span className="visually-hidden">Loading...</span></div>
            </div>
          )}
          {!loading && (data?.txs.length ?? 0) === 0 ? (
            <div className="text-center py-5 text-muted">No queued transactions</div>
          ) : (
            <div className="table-responsive">
              <table className="table table-sm table-hover mb-0 small">
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
                    <th>Arrived</th>
                    {isLoggedIn && <th></th>}
                  </tr>
                </thead>
                <tbody>
                  {(data?.txs ?? []).map((tx: TxPoolTx) => (
                    <tr key={tx.hash}>
                      <td className="font-monospace" title={tx.hash}>{tx.hash.slice(0, 10)}…{tx.hash.slice(-6)}</td>
                      <td className="font-monospace" title={tx.sender}>
                        <a href="#" onClick={(e) => { e.preventDefault(); setSender(tx.sender); setOffset(0); }}>
                          {tx.sender.slice(0, 8)}…{tx.sender.slice(-4)}
                        </a>
                      </td>
                      <td className="text-end">{tx.nonce}</td>
                      <td>{TX_TYPES[tx.type] ?? tx.type}</td>
                      <td className="text-end">{tx.gas.toLocaleString()}</td>
                      <td className="text-end">{weiToGwei(tx.max_fee_per_gas)} / {weiToGwei(tx.max_priority_fee_per_gas)}</td>
                      <td className="text-end">{weiToEth(tx.value_wei)}</td>
                      <td className="text-end">{tx.blobs ?? 0}</td>
                      <td className="text-end">{formatBytes(tx.size)}</td>
                      <td title={`${tx.arrived} (slot ${tx.arrived_slot})`}>{relativeTime(tx.arrived)} <span className="text-muted">s{tx.arrived_slot}</span></td>
                      {isLoggedIn && (
                        <td className="text-end">
                          <button className="btn btn-sm btn-outline-danger py-0" disabled={busy} title="Drop" onClick={() => handleDrop(tx.hash)}>
                            <i className="fas fa-times"></i>
                          </button>
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
        {data && data.total > PAGE_LIMIT && (
          <div className="card-footer">
            <Pagination total={data.total} offset={offset} limit={PAGE_LIMIT} onPageChange={setOffset} />
          </div>
        )}
      </div>
      {chainInfo && stats?.last_admitted_at && (
        <div className="text-muted small mt-2">last admission {relativeTime(stats.last_admitted_at)}</div>
      )}
    </div>
  );
};
