import React, { useState } from 'react';
import { useAuthContext } from '../../context/AuthContext';
import { useLocalBuildStatus } from '../../hooks/useLocalBuildStatus';
import { InfoHint } from '../InfoHint';
import { Tag } from '../ui/Tag';

// ElStatusPanel shows whether the EL behind --el-rpc exposes the testing
// namespace the testing builder needs, how blob transactions are handed to
// it, and lets an operator re-probe after reconfiguring the EL.
export const ElStatusPanel: React.FC = () => {
  const { isLoggedIn } = useAuthContext();
  const { status, probe } = useLocalBuildStatus();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const availability = status?.availability;

  const handleProbe = async () => {
    setBusy(true);
    setError(null);
    try {
      await probe();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  if (!availability) {
    return <p className="bd-muted small mb-0">Loading the EL status…</p>;
  }

  return (
    <div className="sd-panel">
      <p className="bd-muted small">
        <code>testing_buildBlockV1</code> lives in an opt-in RPC namespace on the EL&apos;s JSON-RPC
        port (<code>--el-rpc</code>), not on the engine port. buildoor probes it at startup and every 5
        minutes; the testing builder cannot be enabled while it is missing.
      </p>
      {!availability.configured && (
        <div className="bd-notice mb-3">
          The testing builder needs the EL JSON-RPC (<code>--el-rpc</code>).
        </div>
      )}
      {availability.configured && !availability.available && (
        <div className="sd-alert sd-alert-warn mb-3">
          <div>{availability.reason || 'testing_buildBlockV1 is not available on the EL.'}</div>
          {availability.enable_hint && (
            <div className="mt-1">
              Start the EL{availability.el_code ? ` (${availability.el_code})` : ''} with{' '}
              <code>{availability.enable_hint}</code> and probe again.
            </div>
          )}
        </div>
      )}
      <dl className="bd-kv mb-3">
        <dt>
          Execution client
          <InfoHint>
            The EL client behind <code>--el-rpc</code> (from <code>web3_clientVersion</code>) and
            whether its testing namespace answered the last probe.
          </InfoHint>
        </dt>
        <dd>
          {availability.el_code || '—'}{' '}
          {availability.configured && (
            <Tag tone={availability.available ? 'ok' : 'warn'}>
              {availability.available ? 'available' : 'unavailable'}
            </Tag>
          )}
        </dd>
        <dt>
          Blob transactions
          <InfoHint>
            How blob transactions are handed to this EL: <strong>network</strong> carries the sidecar
            (geth, besu, nethermind), <strong>canonical</strong> strips it (reth, ethrex).{' '}
            <em>no bundle</em>: the EL returns no blobs bundle, so blob transactions are skipped unless
            allowed. Set at startup by <code>--local-build-blob-encoding</code>.
          </InfoHint>
        </dt>
        <dd>
          {availability.configured
            ? `${availability.blob_encoding}${availability.blob_bundle ? '' : ', no bundle'}`
            : '—'}
        </dd>
        <dt>Last probe</dt>
        <dd>
          {availability.checked_at && !availability.checked_at.startsWith('0001')
            ? new Date(availability.checked_at).toLocaleTimeString()
            : 'not probed yet'}
        </dd>
      </dl>
      {isLoggedIn && availability.configured && (
        <button type="button" className="bd-btn" disabled={busy} onClick={handleProbe}>
          <i className="fas fa-rotate" aria-hidden="true"></i>
          {busy ? 'Probing…' : 'Probe now'}
        </button>
      )}
      {error && <div className="sd-field-error mt-2">{error}</div>}
    </div>
  );
};
