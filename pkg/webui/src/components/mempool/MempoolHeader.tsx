import React, { useState } from 'react';
import { useSettings } from '../../hooks/useSettings';
import { InfoHint } from '../InfoHint';
import { Switch } from '../ui/Switch';
import { Tag } from '../ui/Tag';
import { ConfirmButton } from './ConfirmButton';
import { CopyButton } from './CopyButton';
import { ErrorNotice } from './LockNotice';

interface MempoolHeaderProps {
  ingressUrl: string;
  enabled: boolean;
  available: boolean;
  unavailableReason?: string;
  pending: number;
  onClear: () => Promise<string | null>;
}

// MempoolHeader is the page title with the pool state, the ingress URL and,
// for operators, the accept switch (txpool.enabled) and the clear action.
export const MempoolHeader: React.FC<MempoolHeaderProps> = ({
  ingressUrl,
  enabled,
  available,
  unavailableReason,
  pending,
  onClear,
}) => {
  const { isLoggedIn, postSettings } = useSettings();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  const toggle = async (next: boolean) => {
    setBusy(true);
    setMessage(null);
    const result = await postSettings({ 'txpool.enabled': next });
    setBusy(false);
    if (!result.ok) setMessage(result.error ?? 'failed to update txpool.enabled');
  };

  const clear = async () => {
    setMessage(null);
    const err = await onClear();
    if (err) setMessage(err);
  };

  const status = !available ? (
    <Tag tone="danger" title={unavailableReason || 'The transaction pool is not available'}>
      Unavailable
    </Tag>
  ) : enabled ? (
    <Tag tone="ok" title="/rpc admits transactions into this mempool">Accepting</Tag>
  ) : (
    <Tag tone="warn" title="Every /rpc call is refused while the pool is paused">Paused</Tag>
  );

  return (
    <div className="bd-mp-header">
      <div className="bd-mp-header-main">
        <div className="d-flex align-items-center gap-2 flex-wrap">
          <h1 className="bd-page-title">Private mempool</h1>
          {status}
        </div>
        <div className="bd-page-lead bd-mp-ingress">
          <span>Ingress</span>
          <code>{ingressUrl}</code>
          <CopyButton value={ingressUrl} title="Copy the ingress URL" />
        </div>
      </div>
      {isLoggedIn && (
        <div className="bd-mp-header-actions">
          <div className="bd-mp-switch-label">
            <Switch
              checked={enabled}
              label="accepting transactions"
              busy={busy}
              disabled={!enabled && !available}
              title={!enabled && !available ? unavailableReason || 'The transaction pool is not available' : undefined}
              onChange={toggle}
            />
            <span>Accept transactions</span>
            <InfoHint alignEnd>
              <code>txpool.enabled</code>: on, <code>/rpc</code> admits transactions into this
              mempool and serves the read passthrough; off, every <code>/rpc</code> call is refused
              and the testing builder skips the buildoor mempool sources.
            </InfoHint>
          </div>
          <ConfirmButton
            disabled={pending === 0}
            prompt={`Drop all ${pending.toLocaleString()} queued transactions?`}
            confirmLabel="Drop all"
            onConfirm={clear}
          >
            <i className="fas fa-trash"></i>Clear pool
          </ConfirmButton>
        </div>
      )}
      {message && (
        <div className="bd-mp-header-error">
          <ErrorNotice message={message} onDismiss={() => setMessage(null)} />
        </div>
      )}
    </div>
  );
};
