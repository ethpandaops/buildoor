import React, { useEffect, useMemo, useState } from 'react';
import type { BuilderPreference } from '../types';
import { Pagination } from './Pagination';

interface BuilderPreferencesListProps {
  preferences: BuilderPreference[];
  loading?: boolean;
  error?: string | null;
}

function copyToClipboard(text: string) {
  navigator.clipboard.writeText(text).catch((err) => {
    console.error('Failed to copy:', err);
  });
}

function formatEth(gwei: number): string {
  // 1 ETH = 1e9 Gwei
  return (gwei / 1e9).toLocaleString(undefined, { maximumFractionDigits: 9 });
}

export const BuilderPreferencesList: React.FC<BuilderPreferencesListProps> = ({
  preferences,
  loading,
  error,
}) => {
  const [searchTerm, setSearchTerm] = useState('');
  const [offset, setOffset] = useState(0);
  const limit = 50;

  const filtered = useMemo(() => {
    if (!searchTerm) return preferences;
    const term = searchTerm.toLowerCase();
    return preferences.filter(
      (p) =>
        p.validator_pubkey.toLowerCase().includes(term) ||
        String(p.max_execution_payment).includes(term),
    );
  }, [preferences, searchTerm]);

  const total = filtered.length;
  const paged = useMemo(() => filtered.slice(offset, offset + limit), [filtered, offset, limit]);

  useEffect(() => {
    setOffset(0);
  }, [searchTerm]);

  useEffect(() => {
    if (total === 0) {
      if (offset !== 0) setOffset(0);
      return;
    }
    const maxOffset = Math.floor((total - 1) / limit) * limit;
    if (offset > maxOffset) setOffset(maxOffset);
  }, [total, offset, limit]);

  if (loading) {
    return (
      <div className="card mb-3">
        <div className="card-header">
          <h5 className="mb-0">Builder Preferences</h5>
        </div>
        <div className="card-body">
          <div className="text-muted text-center">Loading...</div>
        </div>
      </div>
    );
  }

  if (error && error.includes('not enabled')) {
    return (
      <div className="card mb-3">
        <div className="card-header">
          <h5 className="mb-0">Builder Preferences</h5>
        </div>
        <div className="card-body">
          <div className="text-muted text-center small">
            Builder API not enabled. Run with --builder-api-enabled to receive proposer submissions.
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="card mb-3">
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">Builder Preferences</h5>
        <span className="badge bg-primary">{preferences.length}</span>
      </div>
      <div className="card-body p-2">
        {preferences.length === 0 ? (
          <div className="text-muted text-center small">No builder preferences received yet</div>
        ) : (
          <>
            <div className="mb-2">
              <input
                type="text"
                className="form-control form-control-sm"
                placeholder="Search by validator pubkey or max execution payment..."
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
              />
            </div>

            <div>
              <table className="table table-sm table-borderless mb-0">
                <thead className="sticky-top" style={{ background: 'var(--bs-body-bg)' }}>
                  <tr>
                    <th className="small">Validator Pubkey</th>
                    <th className="small text-end">Max Execution Payment (Gwei)</th>
                    <th className="small text-end">ETH</th>
                  </tr>
                </thead>
                <tbody>
                  {paged.length === 0 ? (
                    <tr>
                      <td colSpan={3} className="text-muted text-center small">
                        No preferences match your search
                      </td>
                    </tr>
                  ) : (
                    paged.map((pref, idx) => (
                      <tr key={idx}>
                        <td className="small font-monospace">
                          <span
                            className="text-info hash-copy-text"
                            style={{ cursor: 'pointer' }}
                            onClick={() => copyToClipboard(pref.validator_pubkey)}
                            title="Click to copy"
                          >
                            {pref.validator_pubkey}
                          </span>
                        </td>
                        <td className="small text-end font-monospace">
                          {pref.max_execution_payment.toLocaleString()}
                        </td>
                        <td className="small text-end font-monospace text-muted">
                          {formatEth(pref.max_execution_payment)}
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>

            <Pagination total={total} offset={offset} limit={limit} onPageChange={setOffset} />
          </>
        )}
      </div>
    </div>
  );
};
