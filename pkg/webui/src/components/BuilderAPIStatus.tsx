import React from 'react';
import type { BuilderAPIStatus as BuilderAPIStatusType } from '../types';

interface BuilderAPIStatusProps {
  status: BuilderAPIStatusType | null;
  loading?: boolean;
}

// Format gwei to readable format
function formatGwei(gwei: number): string {
  if (gwei === 0) return '0';
  return gwei.toLocaleString() + ' Gwei';
}

export const BuilderAPIStatus: React.FC<BuilderAPIStatusProps> = ({ status, loading }) => {
  if (loading) {
    return (
      <div className="card mb-3">
        <div className="card-header">
          <h5 className="mb-0">Builder API Status</h5>
        </div>
        <div className="card-body">
          <div className="text-muted text-center">Loading...</div>
        </div>
      </div>
    );
  }

  if (!status) {
    return (
      <div className="card mb-3">
        <div className="card-header">
          <h5 className="mb-0">Builder API Status</h5>
        </div>
        <div className="card-body">
          <div className="text-muted text-center">Status unavailable</div>
        </div>
      </div>
    );
  }

  return (
    <div className="card mb-3">
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">Builder API Status</h5>
        {status.enabled ? (
          <span className="badge bg-success">Enabled</span>
        ) : (
          <span className="badge bg-secondary">Disabled</span>
        )}
      </div>
      <div className="card-body p-2">
        <table className="table table-sm table-borderless mb-0">
          <tbody>
            {/* Status */}
            <tr>
              <td className="text-muted">Status:</td>
              <td className="text-end">
                {status.enabled ? (
                  <span className="badge bg-success">Running</span>
                ) : (
                  <span className="badge bg-secondary">Not Running</span>
                )}
              </td>
            </tr>

            {/* Port */}
            {status.enabled && status.port > 0 && (
              <tr>
                <td className="text-muted">Port:</td>
                <td className="text-end font-monospace small">{status.port}</td>
              </tr>
            )}

            {/* Validator Count */}
            {status.enabled && (
              <tr>
                <td className="text-muted">Registered Validators:</td>
                <td className="text-end">
                  <span className="badge bg-primary">{status.validator_count}</span>
                </td>
              </tr>
            )}

            {/* Separator */}
            {status.enabled && (
              <tr>
                <td colSpan={2}><hr className="my-1" /></td>
              </tr>
            )}

            {/* Configuration */}
            {status.enabled && (
              <>
                <tr>
                  <td className="text-muted small">Use Proposer Fee Recipient:</td>
                  <td className="text-end small">
                    {status.use_proposer_fee_recipient ? (
                      <span className="badge bg-success">Yes</span>
                    ) : (
                      <span className="badge bg-secondary">No</span>
                    )}
                  </td>
                </tr>
                {status.block_value_subsidy_gwei > 0 && (
                  <tr>
                    <td className="text-muted small">Block Value Subsidy:</td>
                    <td className="text-end small">
                      {formatGwei(status.block_value_subsidy_gwei)}
                    </td>
                  </tr>
                )}
              </>
            )}

            {/* Endpoints info */}
            {status.enabled && (
              <>
                <tr>
                  <td colSpan={2}><hr className="my-1" /></td>
                </tr>
                <tr>
                  <td colSpan={2} className="small text-muted">
                    <div>Endpoints:</div>
                    <div className="font-monospace mt-1">
                      <div>POST /eth/v1/builder/validators</div>
                      <div>GET /eth/v1/builder/status</div>
                      <div>GET /eth/v1/builder/header/...</div>
                      <div>POST /eth/v2/builder/blinded_blocks</div>
                    </div>
                  </td>
                </tr>
              </>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
};
