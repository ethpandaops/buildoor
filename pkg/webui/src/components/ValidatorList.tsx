import React, { useState, useMemo, useEffect } from 'react';
import type { ValidatorRegistration } from '../types';
import { Pagination } from './Pagination';

interface ValidatorListProps {
  validators: ValidatorRegistration[];
  loading?: boolean;
  fullPage?: boolean;
}

// Copy to clipboard helper
function copyToClipboard(text: string) {
  navigator.clipboard.writeText(text).catch((err) => {
    console.error('Failed to copy:', err);
  });
}

export const ValidatorList: React.FC<ValidatorListProps> = ({ validators, loading, fullPage }) => {
  const [searchTerm, setSearchTerm] = useState('');
  const [offset, setOffset] = useState(0);
  const limit = 50;

  const filteredValidators = useMemo(() => {
    if (!searchTerm) return validators;
    const term = searchTerm.toLowerCase();
    return validators.filter(
      (v) =>
        v.pubkey.toLowerCase().includes(term) ||
        v.fee_recipient.toLowerCase().includes(term)
    );
  }, [validators, searchTerm]);

  const total = filteredValidators.length;
  const pagedValidators = useMemo(
    () => filteredValidators.slice(offset, offset + limit),
    [filteredValidators, offset, limit]
  );

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
          <h5 className="mb-0">Registered Validators</h5>
        </div>
        <div className="card-body">
          <div className="text-muted text-center">Loading...</div>
        </div>
      </div>
    );
  }

  return (
    <div className="card mb-3">
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">Registered Validators</h5>
        <span className="badge bg-primary">{validators.length}</span>
      </div>
      <div className="card-body p-2">
        {validators.length === 0 ? (
          <div className="text-muted text-center small">No validators registered</div>
        ) : (
          <>
            {/* Search input */}
            <div className="mb-2">
              <input
                type="text"
                className="form-control form-control-sm"
                placeholder="Search by pubkey or fee recipient..."
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
              />
            </div>

            {/* Validators table */}
            <div>
              <table className="table table-sm table-borderless mb-0">
                <thead className="sticky-top" style={{ background: 'var(--bs-body-bg)' }}>
                  <tr>
                    <th className="small">Pubkey</th>
                    <th className="small">Fee Recipient</th>
                    <th className="small text-end">Gas Limit</th>
                    <th className="small text-end">Registered</th>
                  </tr>
                </thead>
                <tbody>
                  {pagedValidators.length === 0 ? (
                    <tr>
                      <td colSpan={4} className="text-muted text-center small">
                        No validators match your search
                      </td>
                    </tr>
                  ) : (
                    pagedValidators.map((validator, idx) => (
                      <tr key={idx}>
                        <td className="small font-monospace">
                          <span
                            className="text-primary hash-copy-text"
                            style={{ cursor: 'pointer' }}
                            onClick={() => copyToClipboard(validator.pubkey)}
                            title="Click to copy"
                          >
                            {validator.pubkey}
                          </span>
                        </td>
                        <td className="small font-monospace">
                          <span
                            className="text-info hash-copy-text"
                            style={{ cursor: 'pointer' }}
                            onClick={() => copyToClipboard(validator.fee_recipient)}
                            title="Click to copy"
                          >
                            {validator.fee_recipient}
                          </span>
                        </td>
                        <td className="small text-end">
                          {validator.gas_limit.toLocaleString()}
                        </td>
                        <td className="small text-end text-muted">
                          {new Date(validator.timestamp * 1000).toLocaleString()}
                        </td>
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
