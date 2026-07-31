import React, { useEffect, useState } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { BuilderInfo as BuilderInfoType, ServiceStatus, Config } from '../types';
import { CopyableHash } from './CopyableHash';
import { useBuilderKeyActions } from '../hooks/useBuilderKeys';
import { setView } from '../stores/viewStore';

interface BuilderInfoProps {
  builderInfo: BuilderInfoType | null;
  serviceStatus: ServiceStatus | null;
  config: Config | null;
}

// Format gwei to ETH with 4 decimals
function formatGwei(gwei: number): string {
  const eth = gwei / 1e9;
  return eth.toFixed(4);
}

// Format wei to ETH with 4 decimals
function formatWei(wei: string): string {
  const weiNum = BigInt(wei);
  const eth = Number(weiNum) / 1e18;
  return eth.toFixed(4);
}

export const BuilderInfo: React.FC<BuilderInfoProps> = ({ builderInfo, serviceStatus, config }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [toggling, setToggling] = useState(false);
  const [editingLifecycle, setEditingLifecycle] = useState(false);
  const [lcThreshold, setLcThreshold] = useState('');
  const [lcAmount, setLcAmount] = useState('');
  const [editingTarget, setEditingTarget] = useState(false);
  const [targetInput, setTargetInput] = useState('');
  const [targetError, setTargetError] = useState('');
  const keyActions = useBuilderKeyActions();

  const lifecycleAvailable = serviceStatus?.lifecycle_available ?? false;
  const lifecycleEnabled = serviceStatus?.lifecycle_enabled ?? false;
  const registrationState = serviceStatus?.epbs_registration_state;
  // A managed fleet reports totals; a single key reports its own values, which
  // are the same numbers.
  const keyCount = builderInfo?.key_count ?? 0;
  const keyTarget = builderInfo?.key_target ?? 0;
  const isFleet = keyCount > 1 || keyTarget > 1;

  useEffect(() => {
    if (!editingTarget && keyTarget > 0) {
      setTargetInput(String(keyTarget));
    }
  }, [keyTarget, editingTarget]);

  const startEditingLifecycle = () => {
    setLcThreshold(config ? String(config.topup_threshold / 1e9) : '');
    setLcAmount(config ? String(config.topup_amount / 1e9) : '');
    setEditingLifecycle(true);
  };

  const handleLifecycleSave = async () => {
    if (!isLoggedIn) return;
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = await getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }
    try {
      await fetch('/api/config/lifecycle', {
        method: 'POST',
        headers,
        body: JSON.stringify({
          topup_threshold: Math.round(parseFloat(lcThreshold) * 1e9),
          topup_amount: Math.round(parseFloat(lcAmount) * 1e9),
        }),
      });
      setEditingLifecycle(false);
    } catch (err) {
      console.error('Failed to update lifecycle config:', err);
    }
  };

  const handleTargetSave = async () => {
    const target = parseInt(targetInput, 10);
    if (!Number.isFinite(target) || target < 1) {
      setTargetError('target must be at least 1');
      return;
    }

    const result = await keyActions.setTarget(target);
    setTargetError(result.ok ? '' : (result.error ?? 'failed to set target'));

    if (result.ok) {
      setEditingTarget(false);
    }
  };

  const handleLifecycleToggle = async () => {
    if (!isLoggedIn) return;
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = await getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }
    setToggling(true);
    try {
      await fetch('/api/services/toggle', {
        method: 'POST',
        headers,
        body: JSON.stringify({ lifecycle_enabled: !lifecycleEnabled }),
      });
    } catch (err) {
      console.error('Failed to toggle lifecycle:', err);
    } finally {
      setToggling(false);
    }
  };

  if (!builderInfo) {
    return (
      <div className="card mb-3">
        <div className="card-header">
          <h5 className="mb-0">Builder Info</h5>
        </div>
        <div className="card-body">
          <div className="text-muted text-center">Loading...</div>
        </div>
      </div>
    );
  }

  return (
    <div className="card mb-3">
      <div className="card-header d-flex align-items-center">
        <h5 className="mb-0 me-2">Builder Info</h5>
        {isFleet && (
          <span
            className="badge bg-info me-2"
            title="Active builder keys / target key count"
          >
            {builderInfo?.keys_active ?? 0} / {keyTarget} keys
          </span>
        )}
        {lifecycleAvailable && (
          <>
            {lifecycleEnabled ? (
              <span className="badge bg-success">Lifecycle Active</span>
            ) : (
              <span className="badge bg-secondary">Lifecycle Inactive</span>
            )}
            {isLoggedIn && (
              <button
                className={`btn btn-sm ms-auto ${lifecycleEnabled ? 'btn-outline-danger' : 'btn-outline-success'}`}
                onClick={handleLifecycleToggle}
                disabled={toggling}
                title={lifecycleEnabled ? 'Disable lifecycle management' : 'Enable lifecycle management'}
              >
                <i className={`fas ${lifecycleEnabled ? 'fa-pause' : 'fa-play'}`}></i>
              </button>
            )}
          </>
        )}
        {!lifecycleAvailable && (
          <span className="badge bg-dark">No Lifecycle</span>
        )}
      </div>
      <div className="card-body p-2">
        {/* Exited-key warning: per the Gloas spec an exited builder can never be
            reactivated — deposits to the pubkey only top up the exited entry and
            are swept back to the wallet, so top-ups are disabled */}
        {(registrationState === 'exiting' || registrationState === 'exited') && (
          <div className="alert alert-warning py-2 px-2 small mb-2">
            <i className="fas fa-exclamation-triangle me-1"></i>
            <strong>Builder key {registrationState === 'exited' ? 'exited' : 'exiting'} — cannot be reactivated.</strong>{' '}
            Deposits to this key only top up the exited entry and are withdrawn back to the wallet,
            so automatic top-ups are disabled. The key becomes usable for a fresh registration only
            after its registry slot is reused by another builder&apos;s deposit.
          </div>
        )}
        <table className="table table-sm table-borderless mb-0">
          <tbody>
            {/* Builder Identity */}
            <tr>
              <td className="text-muted">Pubkey:</td>
              <td className="text-end font-monospace small">
                <CopyableHash value={builderInfo.builder_pubkey} chars={8} />
              </td>
            </tr>
            <tr>
              <td className="text-muted">Index:</td>
              <td className="text-end">
                {(() => {
                  const state = serviceStatus?.epbs_registration_state;
                  switch (state) {
                    case 'registered':
                      return <span className="badge bg-success">{builderInfo.builder_index}</span>;
                    case 'pending_finalization':
                      return <span className="badge bg-info">#{builderInfo.builder_index} (Pending Finalization)</span>;
                    case 'exiting':
                      return <span className="badge bg-warning text-dark">#{builderInfo.builder_index} (Exiting)</span>;
                    case 'exited':
                      return <span className="badge bg-secondary">#{builderInfo.builder_index} (Exited)</span>;
                    case 'waiting_gloas':
                      return <span className="badge bg-info">Awaiting Gloas</span>;
                    case 'pending':
                      return <span className="badge bg-warning text-dark"><i className="fas fa-spinner fa-spin me-1"></i>Registering...</span>;
                    case 'unregistered':
                      return <span className="badge bg-dark">Unregistered</span>;
                    default:
                      return <span className="badge bg-dark">Unknown</span>;
                  }
                })()}
              </td>
            </tr>

            {/* Target key count. A fleet is maintained against it: raising it
                deposits new keys, lowering it exits surplus ones. */}
            {lifecycleAvailable && (
              <tr>
                <td className="text-muted">Target keys:</td>
                <td className="text-end">
                  {!editingTarget ? (
                    <>
                      {keyTarget}
                      {isLoggedIn && (
                        <button
                          className="btn btn-sm btn-outline-primary ms-1 py-0 px-1"
                          style={{ fontSize: '10px', lineHeight: '14px' }}
                          onClick={() => {
                            setTargetInput(String(keyTarget));
                            setTargetError('');
                            setEditingTarget(true);
                          }}
                        >
                          <i className="fas fa-pencil-alt"></i>
                        </button>
                      )}
                    </>
                  ) : (
                    <div className="d-flex justify-content-end align-items-center gap-1">
                      <input
                        type="number"
                        min={1}
                        className="form-control form-control-sm"
                        style={{ fontSize: '11px', width: '4.5rem' }}
                        value={targetInput}
                        onChange={(e) => setTargetInput(e.target.value)}
                      />
                      <button className="btn btn-sm btn-primary py-0 px-1" onClick={handleTargetSave}>
                        Save
                      </button>
                      <button
                        className="btn btn-sm btn-secondary py-0 px-1"
                        onClick={() => setEditingTarget(false)}
                      >
                        Cancel
                      </button>
                    </div>
                  )}
                </td>
              </tr>
            )}
            {editingTarget && parseInt(targetInput, 10) < keyTarget && (
              <tr>
                <td colSpan={2} className="small text-warning">
                  Lowering the target exits surplus keys, highest index first. An exited builder
                  key can never be reactivated.
                </td>
              </tr>
            )}
            {targetError && (
              <tr>
                <td colSpan={2} className="small text-danger">{targetError}</td>
              </tr>
            )}

            {/* Wallet Info (if lifecycle enabled) */}
            {builderInfo.lifecycle_enabled && builderInfo.wallet_address && (
              <tr>
                <td className="text-muted">Wallet:</td>
                <td className="text-end font-monospace small">
                  <CopyableHash value={builderInfo.wallet_address} chars={6} />
                </td>
              </tr>
            )}

            {/* Separator */}
            <tr>
              <td colSpan={2}><hr className="my-1" /></td>
            </tr>

            {/* CL Balance */}
            <tr>
              <td className="text-muted">{isFleet ? 'CL Balance (all keys):' : 'CL Balance:'}</td>
              <td className="text-end">
                <span className="text-primary fw-bold">
                  {formatGwei(isFleet ? builderInfo.total_balance_gwei : builderInfo.cl_balance_gwei)} ETH
                </span>
              </td>
            </tr>

            {/* Pending Payments (always shown) */}
            <tr>
              <td className="text-muted">Pending Payments:</td>
              <td className="text-end text-warning">
                {(() => {
                  const pending = isFleet
                    ? builderInfo.total_pending_payments_gwei
                    : builderInfo.pending_payments_gwei;
                  return pending > 0 ? `-${formatGwei(pending)} ETH` : '0 ETH';
                })()}
              </td>
            </tr>

            {/* Effective Balance (shown when pending payments reduce it) */}
            {(isFleet ? builderInfo.total_pending_payments_gwei : builderInfo.pending_payments_gwei) > 0 && (
              <tr>
                <td className="text-muted">Effective Balance:</td>
                <td className="text-end text-success fw-bold">
                  {formatGwei(
                    isFleet ? builderInfo.total_effective_gwei : builderInfo.effective_balance_gwei,
                  )} ETH
                </td>
              </tr>
            )}

            {/* Wallet Balance (if lifecycle enabled) */}
            {builderInfo.lifecycle_enabled && builderInfo.wallet_balance_wei && (
              <>
                <tr>
                  <td colSpan={2}><hr className="my-1" /></td>
                </tr>
                <tr>
                  <td className="text-muted">Wallet Balance:</td>
                  <td className="text-end">
                    {formatWei(builderInfo.wallet_balance_wei)} ETH
                  </td>
                </tr>
              </>
            )}

            {/* Epoch Info - show when builder has an index in beacon state */}
            {builderInfo.builder_index > 0 && (
              <>
                <tr>
                  <td colSpan={2}><hr className="my-1" /></td>
                </tr>
                <tr>
                  <td className="text-muted small">Deposit Epoch:</td>
                  <td className="text-end small">{builderInfo.deposit_epoch}</td>
                </tr>
                {builderInfo.withdrawable_epoch > 0 && builderInfo.withdrawable_epoch < 18446744073709551615 && (
                  <tr>
                    <td className="text-muted small text-warning">Withdrawable:</td>
                    <td className="text-end small text-warning">Epoch {builderInfo.withdrawable_epoch}</td>
                  </tr>
                )}
              </>
            )}
            {/* Lifecycle Config (if available) */}
            {lifecycleAvailable && config && (
              <>
                <tr>
                  <td colSpan={2}><hr className="my-1" /></td>
                </tr>
                {!editingLifecycle ? (
                  <>
                    <tr>
                      <td className="text-muted small">Topup Threshold:</td>
                      <td className="text-end small">
                        {formatGwei(config.topup_threshold)} ETH
                        {isLoggedIn && (
                          <button
                            className="btn btn-sm btn-outline-primary ms-1 py-0 px-1"
                            style={{ fontSize: '10px', lineHeight: '14px' }}
                            onClick={() => startEditingLifecycle()}
                          >
                            <i className="fas fa-pencil-alt"></i>
                          </button>
                        )}
                      </td>
                    </tr>
                    <tr>
                      <td className="text-muted small">Topup Amount:</td>
                      <td className="text-end small">{formatGwei(config.topup_amount)} ETH</td>
                    </tr>
                  </>
                ) : (
                  <>
                    <tr>
                      <td className="text-muted small">Threshold (ETH):</td>
                      <td className="text-end">
                        <input
                          type="number"
                          step="0.1"
                          className="form-control form-control-sm"
                          style={{ fontSize: '11px' }}
                          value={lcThreshold}
                          onChange={(e) => setLcThreshold(e.target.value)}
                        />
                      </td>
                    </tr>
                    <tr>
                      <td className="text-muted small">Amount (ETH):</td>
                      <td className="text-end">
                        <input
                          type="number"
                          step="0.1"
                          className="form-control form-control-sm"
                          style={{ fontSize: '11px' }}
                          value={lcAmount}
                          onChange={(e) => setLcAmount(e.target.value)}
                        />
                      </td>
                    </tr>
                    <tr>
                      <td colSpan={2} className="text-end">
                        <button className="btn btn-sm btn-primary me-1" onClick={handleLifecycleSave}>Save</button>
                        <button className="btn btn-sm btn-secondary" onClick={() => setEditingLifecycle(false)}>Cancel</button>
                      </td>
                    </tr>
                  </>
                )}
              </>
            )}
          </tbody>
        </table>

        {/* Per-key operations (deposit, top-up, exit) live on the Builder Keys
            page: with a fleet, "exit the builder" has no single meaning. */}
        {lifecycleAvailable && (
          <div className="border-top pt-2 mt-1 text-end">
            <a
              href="/builder-keys"
              className="btn btn-sm btn-outline-primary"
              onClick={(e) => {
                e.preventDefault();
                setView('builder-keys');
              }}
            >
              <i className="fas fa-key me-1"></i>Manage keys
            </a>
          </div>
        )}
      </div>
    </div>
  );
};
