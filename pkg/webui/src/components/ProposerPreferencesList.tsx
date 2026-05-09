import React, { useEffect, useMemo, useState } from 'react';
import type { ProposerPreference } from '../types';
import { Pagination } from './Pagination';

interface ProposerPreferencesListProps {
  preferences: ProposerPreference[];
  loading?: boolean;
  error?: string | null;
}

function copyToClipboard(text: string) {
  navigator.clipboard.writeText(text).catch((err) => {
    console.error('Failed to copy:', err);
  });
}

export const ProposerPreferencesList: React.FC<ProposerPreferencesListProps> = ({
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
        String(p.validator_index).includes(term) ||
        p.fee_recipient.toLowerCase().includes(term) ||
        String(p.slot).includes(term),
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
          <h5 className="mb-0">Proposer Preferences</h5>
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
          <h5 className="mb-0">Proposer Preferences</h5>
        </div>
        <div className="card-body">
          <div className="text-muted text-center small">
            Proposer preferences service not enabled. Configure P2P peer addresses to enable.
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="card mb-3">
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">Proposer Preferences</h5>
        <span className="badge bg-primary">{preferences.length}</span>
      </div>
      <div className="card-body p-2">
        {preferences.length === 0 ? (
          <div className="text-muted text-center small">No proposer preferences received yet</div>
        ) : (
          <>
            <div className="mb-2">
              <input
                type="text"
                className="form-control form-control-sm"
                placeholder="Search by slot, validator index, or fee recipient..."
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
              />
            </div>

            <div>
              <table className="table table-sm table-borderless mb-0">
                <thead className="sticky-top" style={{ background: 'var(--bs-body-bg)' }}>
                  <tr>
                    <th className="small">Slot</th>
                    <th className="small">Validator Index</th>
                    <th className="small">Fee Recipient</th>
                    <th className="small text-end">Gas Limit</th>
                  </tr>
                </thead>
                <tbody>
                  {paged.length === 0 ? (
                    <tr>
                      <td colSpan={4} className="text-muted text-center small">
                        No preferences match your search
                      </td>
                    </tr>
                  ) : (
                    paged.map((pref, idx) => (
                      <tr key={idx}>
                        <td className="small font-monospace text-muted">{pref.slot}</td>
                        <td className="small font-monospace">{pref.validator_index}</td>
                        <td className="small font-monospace">
                          <span
                            className="text-info hash-copy-text"
                            style={{ cursor: 'pointer' }}
                            onClick={() => copyToClipboard(pref.fee_recipient)}
                            title="Click to copy"
                          >
                            {pref.fee_recipient}
                          </span>
                        </td>
                        <td className="small text-end">{pref.gas_limit.toLocaleString()}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>

            <Pagination
              total={total}
              offset={offset}
              limit={limit}
              onPageChange={setOffset}
            />
          </>
        )}
      </div>
    </div>
  );
};
