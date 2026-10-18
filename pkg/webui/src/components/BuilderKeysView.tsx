import React, { useState } from 'react';
import { BuilderKeysTable } from './BuilderKeysTable';
import { InfoHint } from './InfoHint';
import { useBuilderKeyActions, useBuilderKeys } from '../hooks/useBuilderKeys';
import { openSettings } from '../stores/settingsDrawerStore';
import type { BuilderKeysAggregate } from '../types';

interface BuilderKeysViewProps {
  /**
   * Key set pushed by the SSE stream. It is the live source; the REST fetch
   * supplies the settings and covers the moment before the first event.
   */
  streamKeys: BuilderKeysAggregate | null;
  connectionGeneration: number;
}

function formatGwei(gwei: number): string {
  return (gwei / 1e9).toFixed(4);
}

export const BuilderKeysView: React.FC<BuilderKeysViewProps> = ({
  streamKeys,
  connectionGeneration,
}) => {
  const { keys, aggregate, settings, loading, error, refetch } = useBuilderKeys(
    `${connectionGeneration}:${streamKeys?.managed ?? 0}:${streamKeys?.active ?? 0}:${streamKeys?.target ?? 0}`,
  );
  const actions = useBuilderKeyActions();

  const [busyKey, setBusyKey] = useState<number | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const runAction = async (keyIndex: number, action: () => Promise<{ ok: boolean; error?: string }>) => {
    setBusyKey(keyIndex);
    setActionError(null);

    const result = await action();
    if (!result.ok) {
      setActionError(result.error ?? 'request failed');
    }

    setBusyKey(null);
    refetch();
  };

  return (
    <div className="card">
      <div className="card-header d-flex align-items-center flex-wrap gap-2">
        <h5 className="mb-0 me-2">Builder Keys</h5>
        <span className="badge bg-success">{aggregate.active} active</span>
        <span className="badge bg-secondary">{aggregate.managed} managed</span>
        <span className="badge bg-info">target {settings.target_count}</span>
        <InfoHint>
          <dl>
            <dt>active</dt>
            <dd>registered on chain with a finalized deposit and not exiting: may bid</dd>
            <dt>managed</dt>
            <dd>keys counting toward the target: depositing, pending or active (exiting and exited
              keys never bid again and are replaced)</dd>
            <dt>target</dt>
            <dd><code>builder_keys.target_count</code>: keys the lifecycle manager keeps registered
              and funded (at least 1, at most max key index + 1). Key 0 is the entry key; key n is
              derived from it. Raising it deposits new keys (auto-deposit), lowering it exits the
              highest-index surplus keys (auto-exit, irreversible).</dd>
          </dl>
          Deposits, exits and top-ups only run with the lifecycle manager (<code>--lifecycle</code>).
        </InfoHint>
        {aggregate.depositing > 0 && (
          <span className="badge bg-warning text-dark">
            <i className="fas fa-spinner fa-spin me-1"></i>
            {aggregate.depositing} depositing
          </span>
        )}
        {!settings.auto_deposit && <span className="badge bg-dark">auto-deposit off</span>}
        {!settings.auto_exit && <span className="badge bg-dark">auto-exit off</span>}

        <div className="ms-auto d-flex align-items-center gap-2">
          <span className="text-muted small">
            Total effective:
            <InfoHint alignEnd>
              Sum over all registered keys of the effective balance: the chain balance plus local
              adjustments minus pending (not yet settled) bid payments, i.e. what the fleet can
              still bid against.
            </InfoHint>
          </span>
          <span className="fw-bold">{formatGwei(aggregate.total_effective_gwei)} ETH</span>

          <button type="button" className="bd-btn bd-btn-sm" onClick={() => openSettings('lifecycle', 'fleet')}>
            <i className={`fas ${actions.isLoggedIn ? 'fa-sliders-h' : 'fa-eye'}`} aria-hidden="true"></i>
            Fleet settings
          </button>
        </div>
      </div>

      <div className="card-body p-2">
        <BuilderKeysTable
          keys={keys}
          loading={loading}
          error={error}
          target={settings.target_count}
          isLoggedIn={actions.isLoggedIn}
          busyKey={busyKey}
          actionError={actionError}
          onDeposit={(keyIndex) => runAction(keyIndex, () => actions.depositKey(keyIndex))}
          onTopup={(keyIndex) => runAction(keyIndex, () => actions.topupKey(keyIndex))}
          onExit={(keyIndex, lowerTarget) =>
            runAction(keyIndex, () => actions.exitKey(keyIndex, lowerTarget))
          }
        />
      </div>
    </div>
  );
};
