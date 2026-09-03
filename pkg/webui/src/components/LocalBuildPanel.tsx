import React, { useEffect, useState } from 'react';
import { useSettings } from '../hooks/useSettings';
import { useLocalBuildStatus } from '../hooks/useLocalBuildStatus';
import type { Config, ServiceStatus } from '../types';

interface LocalBuildPanelProps {
  config: Config | null;
  serviceStatus: ServiceStatus | null;
}

const PAYLOAD_SOURCES: Array<{ value: string; label: string; hint: string }> = [
  { value: 'el', label: 'EL payload (local is a shadow)', hint: 'Bids use the engine-API payload; the local payload is built for comparison only.' },
  { value: 'local', label: 'Local payload (strict)', hint: 'Bids use the local payload; a failed or skipped local build leaves the slot without a payload.' },
  { value: 'local_or_el', label: 'Local, else EL', hint: 'Bids use the local payload when it succeeded, otherwise the engine payload.' },
];

const TX_SOURCES: Array<{ value: string; label: string; hint: string }> = [
  { value: 'txpool', label: 'Transaction pool', hint: 'Select from the owned pool (spamoor via /rpc).' },
  { value: 'empty', label: 'Empty block', hint: 'No transactions at all.' },
  { value: 'el_mempool', label: 'EL mempool (testing path)', hint: 'The EL fills the block from its own mempool through testing_buildBlockV1.' },
];

// LocalBuildPanel is the dashboard card of the local build extension
// (testing_buildBlockV1): the probed EL availability with the per-EL enable
// hint, the enable toggle (vetoed server-side while unavailable), the payload
// and transaction source selectors, and the transaction pool toggle.
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
  });
  const [editing, setEditing] = useState(false);

  useEffect(() => {
    if (!editing && localCfg) {
      setForm({
        payload_source: localCfg.payload_source || 'el',
        tx_source: localCfg.tx_source || 'txpool',
        build_el_payload: localCfg.build_el_payload,
        allow_blobs_without_bundle: localCfg.allow_blobs_without_bundle,
      });
    }
  }, [localCfg, editing]);

  const apply = async (settings: Record<string, unknown>) => {
    setBusy(true);
    setMessage(null);
    const err = await postSettings(settings);
    setBusy(false);
    if (err) setMessage(err);
    return !err;
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
        <h5 className="mb-0 me-2">Local Build</h5>
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

          {/* Availability */}
          <div className="section-header mb-1">EL testing namespace</div>
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
                Local build (testing_buildBlockV1)
              </label>
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
                Transaction pool (ingress at <code>/rpc</code>)
              </label>
              {!poolAvailable && (
                <div className="form-text mt-0">{status?.txpool.reason || 'Requires --el-rpc.'}</div>
              )}
            </div>
          </div>

          {/* Sources */}
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Payload &amp; transaction source</div>
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
                  <div className="config-item-label">Payload source</div>
                  <div className="config-item-value">{localCfg?.payload_source || 'el'}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Tx source</div>
                  <div className="config-item-value">{localCfg?.tx_source || 'txpool'}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Keep EL build</div>
                  <div className="config-item-value">{localCfg?.build_el_payload ? 'yes' : 'no'}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Blobs w/o bundle</div>
                  <div className="config-item-value">{localCfg?.allow_blobs_without_bundle ? 'allowed' : 'skipped'}</div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleSave}>
              <div className="mb-2">
                <label className="form-label">Payload source</label>
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
                <label className="form-label">Transaction source</label>
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
                  Keep running the engine build when the payload source is local
                </label>
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
