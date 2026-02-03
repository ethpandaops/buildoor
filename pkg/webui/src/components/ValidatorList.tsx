import React, { useState, useCallback } from 'react';
import type { ValidatorRegistration } from '../types';

interface ValidatorListProps {
  validators: ValidatorRegistration[];
  loading?: boolean;
}

// Truncate hash/address with ellipsis
function truncateHash(hash: string, chars: number = 8): string {
  if (hash.length <= chars * 2 + 2) return hash;
  return `${hash.substring(0, chars + 2)}...${hash.substring(hash.length - chars)}`;
}

// Format timestamp to readable date
function formatTimestamp(timestamp: number): string {
  const date = new Date(timestamp * 1000);
  return date.toLocaleString();
}

// Format gas limit with commas
function formatGasLimit(gasLimit: number): string {
  return gasLimit.toLocaleString();
}

// Copy to clipboard helper
async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch (err) {
    // Fallback for older browsers
    const textArea = document.createElement('textarea');
    textArea.value = text;
    textArea.style.position = 'fixed';
    textArea.style.opacity = '0';
    document.body.appendChild(textArea);
    textArea.select();
    try {
      document.execCommand('copy');
      document.body.removeChild(textArea);
      return true;
    } catch (err) {
      document.body.removeChild(textArea);
      return false;
    }
  }
}

export const ValidatorList: React.FC<ValidatorListProps> = ({ validators, loading }) => {
  const [searchTerm, setSearchTerm] = useState('');
  const [copiedIndex, setCopiedIndex] = useState<number | null>(null);

  const handleCopy = useCallback(async (text: string, index: number) => {
    const success = await copyToClipboard(text);
    if (success) {
      setCopiedIndex(index);
      setTimeout(() => setCopiedIndex(null), 2000);
    }
  }, []);

  // Filter validators based on search term
  const filteredValidators = validators.filter((v) => {
    if (!searchTerm) return true;
    const term = searchTerm.toLowerCase();
    return (
      v.pubkey.toLowerCase().includes(term) ||
      v.fee_recipient.toLowerCase().includes(term)
    );
  });

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
      <div className="card-body">
        {validators.length === 0 ? (
          <div className="text-muted text-center py-3">
            No validators registered yet
          </div>
        ) : (
          <>
            {/* Search input */}
            <div className="mb-3">
              <input
                type="text"
                className="form-control form-control-sm"
                placeholder="Search by pubkey or fee recipient..."
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
              />
            </div>

            {/* Validators table */}
            <div className="table-responsive" style={{ maxHeight: '400px', overflowY: 'auto' }}>
              <table className="table table-sm table-hover mb-0">
                <thead className="table-light sticky-top">
                  <tr>
                    <th style={{ width: '40%' }}>Pubkey</th>
                    <th style={{ width: '30%' }}>Fee Recipient</th>
                    <th style={{ width: '15%' }}>Gas Limit</th>
                    <th style={{ width: '15%' }}>Registered</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredValidators.length === 0 ? (
                    <tr>
                      <td colSpan={4} className="text-muted text-center py-3">
                        No validators match your search
                      </td>
                    </tr>
                  ) : (
                    filteredValidators.map((validator, index) => (
                      <tr key={validator.pubkey}>
                        <td className="font-monospace small">
                          <span
                            title={validator.pubkey}
                            style={{ cursor: 'pointer' }}
                            onClick={() => handleCopy(validator.pubkey, index)}
                            className="text-primary"
                          >
                            {truncateHash(validator.pubkey, 10)}
                          </span>
                          {copiedIndex === index && (
                            <span className="badge bg-success ms-2">Copied!</span>
                          )}
                        </td>
                        <td className="font-monospace small">
                          <span
                            title={validator.fee_recipient}
                            style={{ cursor: 'pointer' }}
                            onClick={() => handleCopy(validator.fee_recipient, index + 10000)}
                            className="text-info"
                          >
                            {truncateHash(validator.fee_recipient, 6)}
                          </span>
                        </td>
                        <td className="small">{formatGasLimit(validator.gas_limit)}</td>
                        <td className="small text-muted">
                          {formatTimestamp(validator.timestamp)}
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </>
        )}
      </div>
    </div>
  );
};
