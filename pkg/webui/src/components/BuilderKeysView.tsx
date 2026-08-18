import React, { useEffect, useState } from 'react';
import { BuilderKeysTable } from './BuilderKeysTable';
import { useBuilderKeyActions, useBuilderKeys } from '../hooks/useBuilderKeys';
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

  const [targetInput, setTargetInput] = useState('');
  const [editingTarget, setEditingTarget] = useState(false);
  const [busyKey, setBusyKey] = useState<number | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  useEffect(() => {
    if (!editingTarget) {
      setTargetInput(String(settings.target_count));
    }
  }, [settings.target_count, editingTarget]);

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

  const saveTarget = async () => {
    const target = parseInt(targetInput, 10);
    if (!Number.isFinite(target) || target < 1) {
      setActionError('target must be at least 1');
      return;
    }

    setActionError(null);

    const result = await actions.setTarget(target);
    if (!result.ok) {
      setActionError(result.error ?? 'failed to set target');
    }

    setEditingTarget(false);
    refetch();
  };

  const lowering = editingTarget && parseInt(targetInput, 10) < settings.target_count;

  return (
    <div className="card">
      <div className="card-header d-flex align-items-center flex-wrap gap-2">
        <h5 className="mb-0 me-2">Builder Keys</h5>
        <span className="badge bg-success">{aggregate.active} active</span>
        <span className="badge bg-secondary">{aggregate.managed} managed</span>
        <span className="badge bg-info">target {settings.target_count}</span>
        {aggregate.depositing > 0 && (
          <span className="badge bg-warning text-dark">
            <i className="fas fa-spinner fa-spin me-1"></i>
            {aggregate.depositing} depositing
          </span>
        )}
        {!settings.auto_deposit && <span className="badge bg-dark">auto-deposit off</span>}
        {!settings.auto_exit && <span className="badge bg-dark">auto-exit off</span>}

        <div className="ms-auto d-flex align-items-center gap-2">
          <span className="text-muted small">Total effective:</span>
          <span className="fw-bold">{formatGwei(aggregate.total_effective_gwei)} ETH</span>

          {actions.isLoggedIn && !editingTarget && (
            <button
              className="btn btn-sm btn-outline-primary"
              onClick={() => {
                setTargetInput(String(settings.target_count));
                setEditingTarget(true);
              }}
            >
              <i className="fas fa-pencil-alt me-1"></i>Target
            </button>
          )}

          {actions.isLoggedIn && editingTarget && (
            <>
              <input
                type="number"
                min={1}
                max={settings.max_index + 1}
                className="form-control form-control-sm"
                style={{ width: '6rem' }}
                value={targetInput}
                onChange={(e) => setTargetInput(e.target.value)}
              />
              <button className="btn btn-sm btn-primary" onClick={saveTarget}>
                Save
              </button>
              <button className="btn btn-sm btn-secondary" onClick={() => setEditingTarget(false)}>
                Cancel
              </button>
            </>
          )}
        </div>
      </div>

      {lowering && (
        <div className="alert alert-warning rounded-0 mb-0 py-2 small">
          <i className="fas fa-exclamation-triangle me-1"></i>
          Lowering the target exits surplus keys, highest index first. An exited builder key can
          never be reactivated.
        </div>
      )}

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
