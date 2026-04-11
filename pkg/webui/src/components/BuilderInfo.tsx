import React, { useState } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { BuilderInfo as BuilderInfoType, ServiceStatus, Config } from '../types';

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

// Truncate hash/address with ellipsis
function truncateHash(hash: string, chars: number = 8): string {
  if (hash.length <= chars * 2 + 2) return hash;
  return `${hash.substring(0, chars + 2)}...${hash.substring(hash.length - chars)}`;
}

// CopyableHash renders a truncated hash/address that copies the full value on click.
const CopyableHash: React.FC<{ value: string; chars?: number }> = ({ value, chars = 8 }) => {
  const [copied, setCopied] = React.useState(false);
  const handleClick = () => {
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };
  return (
    <span
      title={`${value}\nClick to copy`}
      onClick={handleClick}
      style={{ cursor: 'pointer' }}
      className={copied ? 'text-success' : ''}
    >
      {copied ? 'Copied!' : truncateHash(value, chars)}
    </span>
  );
};

export const BuilderInfo: React.FC<BuilderInfoProps> = ({ builderInfo, serviceStatus, config }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [toggling, setToggling] = useState(false);
  const [editingLifecycle, setEditingLifecycle] = useState(false);
  const [lcThreshold, setLcThreshold] = useState('');
  const [lcAmount, setLcAmount] = useState('');

  const lifecycleAvailable = serviceStatus?.lifecycle_available ?? false;
  const lifecycleEnabled = serviceStatus?.lifecycle_enabled ?? false;

  const startEditingLifecycle = () => {
    setLcThreshold(config ? String(config.topup_threshold / 1e9) : '');
    setLcAmount(config ? String(config.topup_amount / 1e9) : '');
    setEditingLifecycle(true);
  };

  const handleLifecycleSave = async () => {
    const authToken = getAuthHeader();
    if (!authToken) return;
    try {
      await fetch('/api/config/lifecycle', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${authToken}` },
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

  const handleLifecycleToggle = async () => {
    const authToken = getAuthHeader();
    if (!authToken) return;
    setToggling(true);
    try {
      await fetch('/api/services/toggle', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${authToken}`,
        },
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
              <td className="text-muted">CL Balance:</td>
              <td className="text-end">
                <span className="text-primary fw-bold">
                  {formatGwei(builderInfo.cl_balance_gwei)} ETH
                </span>
              </td>
            </tr>

            {/* Pending Payments (always shown) */}
            <tr>
              <td className="text-muted">Pending Payments:</td>
              <td className="text-end text-warning">
                {builderInfo.pending_payments_gwei > 0
                  ? `-${formatGwei(builderInfo.pending_payments_gwei)} ETH`
                  : '0 ETH'}
              </td>
            </tr>

            {/* Effective Balance (shown when pending payments reduce it) */}
            {builderInfo.pending_payments_gwei > 0 && (
              <tr>
                <td className="text-muted">Effective Balance:</td>
                <td className="text-end text-success fw-bold">
                  {formatGwei(builderInfo.effective_balance_gwei)} ETH
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
      </div>
    </div>
  );
};
