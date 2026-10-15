import React, { useEffect, useState } from 'react';
import { useSettings } from '../hooks/useSettings';
import { useLocalBuildStatus } from '../hooks/useLocalBuildStatus';
import { InfoHint } from './InfoHint';
import type { Config, ServiceStatus } from '../types';

interface LocalBuildPanelProps {
  config: Config | null;
  serviceStatus: ServiceStatus | null;
}

// Both builds run on the EL. The ENGINE build is the regular one: buildoor
// sends forkchoiceUpdated + getPayload and the EL picks the transactions from
// its own mempool. The CUSTOM build (testing_buildBlockV1) is the extension:
// buildoor hands the EL the transaction list to execute.
const PAYLOAD_SOURCES: Array<{ value: string; label: string; hint: string }> = [
  { value: 'el', label: 'Engine block', hint: 'Bids and reveals use the engine block. The custom block is built next to it for comparison only.' },
  { value: 'local', label: 'Custom block', hint: 'Bids and reveals use the custom block. When it fails or is skipped, the slot has no payload (no engine fallback).' },
  { value: 'local_or_el', label: 'Custom block, else engine block', hint: 'Bids and reveals use the custom block when it was built, otherwise the engine block.' },
  { value: 'higher_value', label: 'Higher-value block', hint: 'Both blocks are built; bids and reveals use the one with the higher block value (the engine block on a tie, the other one when a build fails).' },
];

const TX_SOURCES: Array<{ value: string; label: string; hint: string }> = [
  { value: 'txpool', label: 'buildoor mempool', hint: "Transactions from buildoor's own mempool (sent to /rpc, see the Mempool tab). The EL's mempool is not used." },
  { value: 'el_mempool', label: 'EL mempool', hint: "The EL fills the block from its own mempool, like the engine build does (useful to compare the two build paths)." },
  { value: 'empty', label: 'None (empty block)', hint: 'No transactions at all.' },
];

const optionLabel = (options: Array<{ value: string; label: string }>, value: string | undefined, fallback: string) =>
  options.find((o) => o.value === (value || fallback))?.label ?? value ?? fallback;

// LocalBuildPanel is the dashboard card of the custom build extension
// (testing_buildBlockV1, settings under local_build.*): the probed EL
// availability with the per-EL enable hint, the enable toggle (vetoed
// server-side while unavailable), which block feeds bids, where the custom
// block's transactions come from, and the buildoor mempool toggle.
export const LocalBuildPanel: React.FC<LocalBuildPanelProps> = ({ config, serviceStatus }) => {
  const { isLoggedIn, postSettings } = useSettings();
  const [collapsed, setCollapsed] = useState(true);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  const { status, loading, probe } = useLocalBuildStatus(
    `${serviceStatus?.local_build_available}/${serviceStatus?.local_build_enabled}/${serviceStatus?.txpool_enabled}`
  );

  const localCfg = config?.local_build;
  const poolCfg = config?.txpool;
  const availability = status?.availability;
  const available = availability?.available ?? false;

  const [form, setForm] = useState({
    payload_source: 'el',
    tx_source: 'txpool',
    build_el_payload: true,
    allow_blobs_without_bundle: false,
    max_attempts: 3,
  });
  const [editing, setEditing] = useState(false);

  useEffect(() => {
    if (!editing && localCfg) {
      setForm({
        payload_source: localCfg.payload_source || 'el',
        tx_source: localCfg.tx_source || 'txpool',
        build_el_payload: localCfg.build_el_payload,
        allow_blobs_without_bundle: localCfg.allow_blobs_without_bundle,
        max_attempts: localCfg.max_attempts || 3,
      });
    }
  }, [localCfg, editing]);

  const apply = async (settings: Record<string, unknown>) => {
    setBusy(true);
    setMessage(null);
    const result = await postSettings(settings);
    setBusy(false);
    if (!result.ok) setMessage(result.error ?? 'failed to update local build settings');
    return result.ok;
  };

  const handleProbe = async () => {
    setBusy(true);
    setMessage(null);
    try {
      const result = await probe();
      if (result && !result.available) setMessage(result.reason || 'testing_buildBlockV1 unavailable');
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const ok = await apply({
      'local_build.payload_source': form.payload_source,
      'local_build.tx_source': form.tx_source,
      'local_build.build_el_payload': form.build_el_payload,
      'local_build.allow_blobs_without_bundle': form.allow_blobs_without_bundle,
      'local_build.max_attempts': form.max_attempts,
    });
    if (ok) setEditing(false);
  };

  const enabled = localCfg?.enabled ?? false;
  const poolEnabled = poolCfg?.enabled ?? false;
  const poolAvailable = status?.txpool.available ?? false;

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} me-2`}></i>
        <h5 className="mb-0 me-2">Custom Build</h5>
        <span className="ms-auto d-flex gap-1">
          {loading ? (
            <span className="badge bg-secondary">…</span>
          ) : !availability?.configured ? (
            <span className="badge bg-secondary" title="No --el-rpc configured">No EL RPC</span>
          ) : available ? (
            <span className="badge bg-success" title="testing_buildBlockV1 available">EL ready</span>
          ) : (
            <span className="badge bg-warning text-dark" title={availability?.reason}>EL unavailable</span>
          )}
          <span className={`badge ${enabled ? 'bg-success' : 'bg-secondary'}`}>
            {enabled ? 'Enabled' : 'Disabled'}
          </span>
        </span>
      </div>

      {!collapsed && (
        <div className="card-body">
          {message && (
            <div className="alert alert-warning small py-1 px-2 mb-2">{message}</div>
          )}

          <div className="small text-muted mb-2">
            A second block per slot, next to the regular <strong>engine build</strong> (where the EL
            picks the transactions from its own mempool): buildoor hands the EL a transaction list
            through <code>testing_buildBlockV1</code> and the EL executes exactly that list.
          </div>

          {/* Availability */}
          <div className="section-header mb-1">
            EL testing namespace
            <InfoHint>
              <code>testing_buildBlockV1</code> lives in an opt-in RPC namespace on the EL&apos;s
              JSON-RPC port (<code>--el-rpc</code>), not on the engine port. buildoor probes it at
              startup and every 5 minutes; the custom build cannot be enabled while it is missing.
            </InfoHint>
          </div>
          {availability && !available && availability.configured && (
            <div className="alert alert-warning small py-2 px-2 mb-2">
              <div>
                <i className="fas fa-triangle-exclamation me-1"></i>
                {availability.reason || 'testing_buildBlockV1 is not available on the EL.'}
              </div>
              {availability.enable_hint && (
                <div className="mt-1">
                  Start the EL{availability.el_code ? ` (${availability.el_code})` : ''} with{' '}
                  <code>{availability.enable_hint}</code> and probe again.
                </div>
              )}
            </div>
          )}
          {availability && !availability.configured && (
            <div className="alert alert-secondary small py-2 px-2 mb-2">
              The local build needs the EL JSON-RPC (<code>--el-rpc</code>): testing_buildBlockV1
              lives on the public RPC port, not the engine port.
            </div>
          )}
          <div className="row g-2 mb-3">
            <div className="col-6">
              <div className="config-item">
                <div className="config-item-label">EL</div>
                <div className="config-item-value">
                  {availability?.el_code || '—'}{' '}
                  {availability?.configured && (
                    <span className={`badge ${available ? 'bg-success' : 'bg-warning text-dark'}`}>
                      {available ? 'available' : 'unavailable'}
                    </span>
                  )}
                </div>
              </div>
            </div>
            <div className="col-6">
              <div className="config-item">
                <div className="config-item-label">Blob txs</div>
                <div className="config-item-value">
                  {availability?.configured
                    ? `${availability.blob_encoding}${availability.blob_bundle ? '' : ', no bundle'}`
                    : '—'}
                </div>
              </div>
            </div>
            <div className="col-12 d-flex align-items-center gap-2">
              <span className="text-muted small">
                {availability?.checked_at && !availability.checked_at.startsWith('0001')
                  ? `probed ${new Date(availability.checked_at).toLocaleTimeString()}`
                  : 'not probed yet'}
              </span>
              {isLoggedIn && availability?.configured && (
                <button className="btn btn-sm btn-outline-secondary ms-auto" disabled={busy} onClick={handleProbe}>
                  <i className="fas fa-rotate me-1"></i>Probe now
                </button>
              )}
            </div>
          </div>

          {/* Toggles */}
          <div className="section-header mb-1">Extensions</div>
          <div className="mb-3">
            <div className="form-check form-switch">
              <input
                className="form-check-input"
                type="checkbox"
                id="local-build-enabled"
                checked={enabled}
                disabled={!isLoggedIn || busy || (!enabled && !available)}
                onChange={(e) => apply({ 'local_build.enabled': e.target.checked })}
              />
              <label className="form-check-label" htmlFor="local-build-enabled">
                Custom build (testing_buildBlockV1)
              </label>
              <InfoHint>
                Builds the custom block every slot, alongside the engine build. Whether bids use it
                is decided by <strong>Bid with</strong> below. Per-slot overrides live in the action
                plan&apos;s build category.
              </InfoHint>
              {!available && !enabled && (
                <div className="form-text mt-0">Cannot enable while the EL does not expose the testing namespace.</div>
              )}
            </div>
            <div className="form-check form-switch">
              <input
                className="form-check-input"
                type="checkbox"
                id="txpool-enabled"
                checked={poolEnabled}
                disabled={!isLoggedIn || busy || (!poolEnabled && !poolAvailable)}
                onChange={(e) => apply({ 'txpool.enabled': e.target.checked })}
              />
              <label className="form-check-label" htmlFor="txpool-enabled">
                buildoor mempool (ingress at <code>/rpc</code>)
              </label>
              <InfoHint>
                buildoor&apos;s own mempool, separate from the EL&apos;s: transactions sent to{' '}
                <code>/rpc</code> stay private and only land through custom blocks buildoor wins.
                While it is off, <code>/rpc</code> refuses every call.
              </InfoHint>
              {!poolAvailable && (
                <div className="form-text mt-0">{status?.txpool.reason || 'Requires --el-rpc.'}</div>
              )}
            </div>
          </div>

          {/* Post-inclusion plan checks */}
          {status?.plan_checks && (
            <>
              <div className="section-header mb-1">Tx plan checks</div>
              <div className="row g-2 mb-3">
                <div className="col-4">
                  <div className="config-item">
                    <div className="config-item-label">Checked</div>
                    <div className="config-item-value">{status.plan_checks.checked}</div>
                  </div>
                </div>
                <div className="col-4">
                  <div className="config-item">
                    <div className="config-item-label">Match</div>
                    <div className="config-item-value text-success">{status.plan_checks.match}</div>
                  </div>
                </div>
                <div className="col-4">
                  <div className="config-item">
                    <div className="config-item-label">Failed</div>
                    <div className={`config-item-value ${
                      status.plan_checks.mismatch + status.plan_checks.block_not_found + status.plan_checks.missed + status.plan_checks.orphaned > 0
                        ? 'text-danger' : ''}`}>
                      {status.plan_checks.mismatch + status.plan_checks.block_not_found + status.plan_checks.missed + status.plan_checks.orphaned}
                    </div>
                  </div>
                </div>
                <div className="col-12 form-text mt-0">
                  Every included locally built block is re-read from the EL and compared with the
                  submitted transaction list (order and count).
                </div>
              </div>
            </>
          )}

          {/* Sources */}
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Bid with &amp; custom block transactions</div>
            {isLoggedIn && !editing && (
              <button className="btn btn-sm btn-outline-primary" onClick={() => setEditing(true)}>
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editing ? (
            <div className="row g-2">
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Bid with</div>
                  <div className="config-item-value">{optionLabel(PAYLOAD_SOURCES, localCfg?.payload_source, 'el')}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Custom block txs</div>
                  <div className="config-item-value">{optionLabel(TX_SOURCES, localCfg?.tx_source, 'txpool')}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Keep engine build</div>
                  <div className="config-item-value">{localCfg?.build_el_payload ? 'yes' : 'no'}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Blobs w/o bundle</div>
                  <div className="config-item-value">{localCfg?.allow_blobs_without_bundle ? 'allowed' : 'skipped'}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Max attempts</div>
                  <div className="config-item-value">{localCfg?.max_attempts ?? '—'}</div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleSave}>
              <div className="mb-2">
                <label className="form-label">
                  Bid with
                  <InfoHint>
                    Which of the two blocks feeds bids, Builder API headers and the reveal
                    (<code>local_build.payload_source</code>).
                    <dl>
                      {PAYLOAD_SOURCES.map((o) => (
                        <React.Fragment key={o.value}>
                          <dt>{o.label}</dt>
                          <dd>{o.hint}</dd>
                        </React.Fragment>
                      ))}
                    </dl>
                  </InfoHint>
                </label>
                <select
                  className="form-select form-select-sm"
                  value={form.payload_source}
                  onChange={(e) => setForm({ ...form, payload_source: e.target.value })}
                >
                  {PAYLOAD_SOURCES.map((o) => (
                    <option key={o.value} value={o.value}>{o.label}</option>
                  ))}
                </select>
                <div className="form-text">{PAYLOAD_SOURCES.find((o) => o.value === form.payload_source)?.hint}</div>
              </div>
              <div className="mb-2">
                <label className="form-label">
                  Custom block transactions
                  <InfoHint>
                    Where the custom block&apos;s transactions come from
                    (<code>local_build.tx_source</code>). One source per block: the two mempools are
                    not combined. The engine block always uses the EL mempool. Per-slot action plans
                    can also hand an exact list (<em>explicit</em>, <em>queued</em>).
                    <dl>
                      {TX_SOURCES.map((o) => (
                        <React.Fragment key={o.value}>
                          <dt>{o.label}</dt>
                          <dd>{o.hint}</dd>
                        </React.Fragment>
                      ))}
                    </dl>
                  </InfoHint>
                </label>
                <select
                  className="form-select form-select-sm"
                  value={form.tx_source}
                  onChange={(e) => setForm({ ...form, tx_source: e.target.value })}
                >
                  {TX_SOURCES.map((o) => (
                    <option key={o.value} value={o.value}>{o.label}</option>
                  ))}
                </select>
                <div className="form-text">{TX_SOURCES.find((o) => o.value === form.tx_source)?.hint}</div>
              </div>
              <div className="form-check mb-2">
                <input
                  className="form-check-input"
                  type="checkbox"
                  id="local-build-el-payload"
                  checked={form.build_el_payload}
                  onChange={(e) => setForm({ ...form, build_el_payload: e.target.checked })}
                />
                <label className="form-check-label" htmlFor="local-build-el-payload">
                  Keep running the engine build when bidding with the custom block
                </label>
                <InfoHint>
                  Only matters for <em>Bid with: Custom block</em>. On, the engine block is still built
                  for comparison. The other choices always build it. Off saves the EL the second build.
                </InfoHint>
              </div>
              <div className="form-check mb-2">
                <input
                  className="form-check-input"
                  type="checkbox"
                  id="local-build-allow-blobs"
                  checked={form.allow_blobs_without_bundle}
                  onChange={(e) => setForm({ ...form, allow_blobs_without_bundle: e.target.checked })}
                />
                <label className="form-check-label" htmlFor="local-build-allow-blobs">
                  Include blob txs on ELs without a blobs bundle (blobs cannot be revealed)
                </label>
                <InfoHint>
                  Some ELs (reth, ethrex) return no blobs bundle from <code>testing_buildBlockV1</code>,
                  so the blob sidecars of a custom block are unknown. Off skips blob transactions on
                  those ELs; on includes them anyway, for testing a reveal without blob data.
                </InfoHint>
              </div>
              <div className="mb-2">
                <label className="form-label" htmlFor="local-build-max-attempts">
                  Max attempts
                  <InfoHint>
                    For the buildoor mempool source: when the EL refuses the list, the offending
                    transactions are dropped (and get a strike in the pool) and the build retries, up
                    to this many calls. Exact lists from action plans never retry.
                  </InfoHint>
                </label>
                <input
                  type="number"
                  min={1}
                  className="form-control form-control-sm"
                  id="local-build-max-attempts"
                  value={form.max_attempts}
                  onChange={(e) => setForm({ ...form, max_attempts: Number(e.target.value) })}
                />
                <div className="form-text">
                  testing_buildBlockV1 calls per txpool build: a refused list drops the offending
                  transactions and retries. Exact lists never retry.
                </div>
              </div>
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary" disabled={busy}>Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditing(false)}>
                  Cancel
                </button>
              </div>
            </form>
          )}
        </div>
      )}
    </div>
  );
};
