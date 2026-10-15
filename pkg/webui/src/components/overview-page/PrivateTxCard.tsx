import React from 'react';
import { CopyField } from './CopyField';
import type { TxPoolConfig } from '../../types';

interface PrivateTxCardProps {
  open: boolean;
  txpool?: TxPoolConfig;
}

function rate(perSecond: number | undefined): string {
  if (!perSecond) return 'unlimited';
  return `${perSecond.toLocaleString()} calls/s`;
}

export const PrivateTxCard: React.FC<PrivateTxCardProps> = ({ open, txpool }) => {
  const endpoint = `${window.location.origin}/rpc`;
  const apiKeyOnly = txpool?.auth === 'api_key';
  const sharedAnon = txpool?.rpc_anon_scope === 'shared';

  return (
    <section className="card" aria-labelledby="ov-private-title">
      <div className="card-body">
        <h2 id="ov-private-title" className="bd-card-title">
          Send private transactions
        </h2>
        {!open ? (
          <p className="bd-muted ov-small mt-2 mb-0">This builder does not accept private transactions right now.</p>
        ) : (
          <>
            <p className="ov-small mt-2">
              Point your wallet or tool at this JSON-RPC endpoint. Transactions sent with{' '}
              <code>eth_sendRawTransaction</code> wait in buildoor's own mempool and are not shared with other nodes; they
              become public when a block buildoor built with them is published.
            </p>
            <CopyField value={endpoint} label="RPC endpoint" />
            <dl className="bd-kv ov-facts">
              <dt>Access</dt>
              <dd>{apiKeyOnly ? 'API key required' : 'open to everyone'}</dd>
              {!apiKeyOnly && (
                <>
                  <dt>Anonymous rate limit</dt>
                  <dd>
                    {rate(txpool?.rpc_anon_rate)}
                    {txpool?.rpc_anon_rate ? (sharedAnon ? ', shared' : ' per IP') : ''}
                  </dd>
                </>
              )}
              <dt>API key rate limit</dt>
              <dd>{rate(txpool?.rpc_key_rate)}</dd>
              <dt>Pool contents</dt>
              <dd>{apiKeyOnly ? 'keys see their own txs' : 'hidden from anonymous callers'}</dd>
            </dl>
            <p className="bd-muted ov-small mt-2 mb-0">
              {apiKeyOnly
                ? 'Send the key as an X-API-Key or Bearer header, or append it to the URL (/rpc/<key>). '
                : 'Anonymous callers cannot read the pool back; an API key sees the transactions it submitted. '}
              Keys are issued by the operator.
            </p>
          </>
        )}
      </div>
    </section>
  );
};
