import React, { useMemo } from 'react';
import { CopyButton, CopyField, shortHash } from './CopyField';
import type { BuilderKeyState } from '../../types';

interface ValidatorsCardProps {
  builderAPI: boolean;
  epbsScheduled: boolean;
  builderUrl?: string;
  builderPubkey?: string;
  builderKeys: BuilderKeyState[];
}

function parseUrl(value: string): URL | null {
  try {
    return new URL(value);
  } catch {
    return null;
  }
}

export const ValidatorsCard: React.FC<ValidatorsCardProps> = ({
  builderAPI,
  epbsScheduled,
  builderUrl,
  builderPubkey,
  builderKeys,
}) => {
  const url = builderUrl || window.location.origin;
  const parsed = parseUrl(url);
  const relayUrl =
    builderPubkey && parsed ? `${parsed.protocol}//${builderPubkey}@${parsed.host}${parsed.pathname.replace(/\/$/, '')}` : null;

  const builderIndexes = useMemo(
    () =>
      builderKeys
        .filter((k) => k.has_builder_index && (k.status === 'active' || k.status === 'pending'))
        .map((k) => k.builder_index)
        .sort((a, b) => a - b),
    [builderKeys],
  );

  return (
    <section className="card" aria-labelledby="ov-validators-title">
      <div className="card-body">
        <h2 id="ov-validators-title" className="bd-card-title">
          For validators
        </h2>
        {builderAPI && (
          <>
            <p className="ov-small mt-2">
              Register your validators with this builder to receive its bids when you propose.
            </p>
            <h3 className="bd-group-title">Builder URL</h3>
            <CopyField value={url} label="builder URL" />
            {builderPubkey && (
              <>
                <h3 className="bd-group-title mt-3">Builder pubkey</h3>
                <div className="ov-copy-field">
                  <code className="ov-copy-value" title={builderPubkey}>
                    {shortHash(builderPubkey, 10)}
                  </code>
                  <CopyButton value={builderPubkey} label="builder pubkey" />
                </div>
              </>
            )}
            {relayUrl && (
              <>
                <h3 className="bd-group-title mt-3">mev-boost relay URL</h3>
                <CopyField value={relayUrl} label="mev-boost relay URL" />
              </>
            )}
          </>
        )}
        {epbsScheduled && (
          <p className="bd-muted ov-small mt-3 mb-0">
            After Gloas, buildoor also gossips p2p bids from builder keys derived from its entry key
            {builderIndexes.length > 0 ? (
              <>
                {' '}
                (builder {builderIndexes.length === 1 ? 'index' : 'indexes'}{' '}
                <span className="ov-num">{builderIndexes.join(', ')}</span>)
              </>
            ) : null}
            .
          </p>
        )}
      </div>
    </section>
  );
};
