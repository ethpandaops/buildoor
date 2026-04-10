import React from 'react';
import type { BuilderInfo as BuilderInfoType, ServiceStatus } from '../types';

interface BuilderInfoProps {
  builderInfo: BuilderInfoType | null;
  serviceStatus: ServiceStatus | null;
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

export const BuilderInfo: React.FC<BuilderInfoProps> = ({ builderInfo, serviceStatus }) => {
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
      <div className="card-header">
        <h5 className="mb-0">Builder Info</h5>
      </div>
      <div className="card-body p-2">
        <table className="table table-sm table-borderless mb-0">
          <tbody>
            {/* Builder Identity */}
            <tr>
              <td className="text-muted">Pubkey:</td>
              <td className="text-end font-monospace small">
                <span title={builderInfo.builder_pubkey}>
                  {truncateHash(builderInfo.builder_pubkey, 8)}
                </span>
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
                      return <span className="badge bg-warning text-dark">Registering...</span>;
                    default:
                      return <span className="badge bg-warning text-dark">Not Registered</span>;
                  }
                })()}
              </td>
            </tr>

            {/* Wallet Info (if lifecycle enabled) */}
            {builderInfo.lifecycle_enabled && builderInfo.wallet_address && (
              <tr>
                <td className="text-muted">Wallet:</td>
                <td className="text-end font-monospace small">
                  <span title={builderInfo.wallet_address}>
                    {truncateHash(builderInfo.wallet_address, 6)}
                  </span>
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

            {/* Pending Deposit (if any) */}
            {builderInfo.lifecycle_enabled && builderInfo.pending_deposit_gwei && builderInfo.pending_deposit_gwei > 0 && (
              <tr>
                <td className="text-muted">Pending Deposit:</td>
                <td className="text-end text-info">
                  +{formatGwei(builderInfo.pending_deposit_gwei)} ETH
                </td>
              </tr>
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
          </tbody>
        </table>
      </div>
    </div>
  );
};
