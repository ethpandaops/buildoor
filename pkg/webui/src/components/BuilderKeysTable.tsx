import React, { useState } from 'react';
import { CopyableHash } from './CopyableHash';
import { InfoHint } from './InfoHint';
import type { BuilderKeyState, BuilderKeyStatus } from '../types';

interface BuilderKeysTableProps {
  keys: BuilderKeyState[];
  loading: boolean;
  error: string | null;
  target: number;
  isLoggedIn: boolean;
  busyKey: number | null;
  actionError: string | null;
  onDeposit: (keyIndex: number) => void;
  onTopup: (keyIndex: number) => void;
  onExit: (keyIndex: number, lowerTarget: boolean) => void;
}

const FAR_FUTURE_EPOCH = 18446744073709551615;

const STATUS_BADGE: Record<BuilderKeyStatus, { className: string; label: string }> = {
  unused: { className: 'bg-dark', label: 'Unused' },
  depositing: { className: 'bg-warning text-dark', label: 'Depositing' },
  pending: { className: 'bg-info', label: 'Pending' },
  active: { className: 'bg-success', label: 'Active' },
  exiting: { className: 'bg-warning text-dark', label: 'Exiting' },
  exited: { className: 'bg-secondary', label: 'Exited' },
  withdrawn: { className: 'bg-dark', label: 'Withdrawn' },
};

function formatGwei(gwei: number): string {
  return (gwei / 1e9).toFixed(4);
}

export const BuilderKeysTable: React.FC<BuilderKeysTableProps> = ({
  keys,
  loading,
  error,
  target,
  isLoggedIn,
  busyKey,
  actionError,
  onDeposit,
  onTopup,
  onExit,
}) => {
  // Key index whose exit confirmation is open; the confirmation names the
  // consequences because an exit can never be undone.
  const [confirmExit, setConfirmExit] = useState<number | null>(null);
  const [lowerTarget, setLowerTarget] = useState(true);

  if (error) {
    return <div className="alert alert-warning">{error}</div>;
  }

  if (loading && keys.length === 0) {
    return <div className="text-muted text-center py-4">Loading builder keys...</div>;
  }

  if (keys.length === 0) {
    return <div className="text-muted text-center py-4">No builder keys derived yet.</div>;
  }

  return (
    <>
      {actionError && <div className="alert alert-danger py-2 small">{actionError}</div>}
      <div className="table-responsive">
        <table className="table table-sm table-hover align-middle mb-0">
          <thead>
            <tr>
              <th style={{ width: '4rem' }}>
                #
                <InfoHint>
                  Derivation index of the key. 0 is the entry key (<code>--builder-privkey</code> or
                  the mnemonic key); n &ge; 1 is derived from it. Stable forever, unlike the builder
                  index.
                </InfoHint>
              </th>
              <th>Pubkey</th>
              <th>
                Status
                <InfoHint>
                  <dl>
                    <dt>Unused</dt>
                    <dd>derived, never deposited</dd>
                    <dt>Depositing</dt>
                    <dd>deposit submitted, not in the builder registry yet</dd>
                    <dt>Pending</dt>
                    <dd>registered, deposit epoch not finalized: may not bid yet</dd>
                    <dt>Active</dt>
                    <dd>registered, finalized, not exiting: bids</dd>
                    <dt>Exiting</dt>
                    <dd>exit initiated, withdrawable epoch not reached; can never be reactivated</dd>
                    <dt>Exited</dt>
                    <dd>withdrawable epoch passed, entry still in the registry</dd>
                    <dt>Withdrawn</dt>
                    <dd>pubkey has left the registry: depositable again</dd>
                  </dl>
                </InfoHint>
              </th>
              <th className="text-end">
                Builder index
                <InfoHint>
                  On-chain builder registry index, assigned at deposit. Reused by other builders
                  after an exit, so it is not tied to the key forever. — = not registered.
                </InfoHint>
              </th>
              <th className="text-end">
                Balance
                <InfoHint>Builder balance in the beacon state, in ETH.</InfoHint>
              </th>
              <th className="text-end">
                Pending
                <InfoHint>
                  Payments (ETH) owed for won bids that the chain has not settled yet; they are
                  deducted from the effective balance.
                </InfoHint>
              </th>
              <th className="text-end">
                Effective
                <InfoHint alignEnd>
                  What the key can still bid against (ETH): balance plus local adjustments minus
                  pending payments. Keys that cannot cover a bid are picked last.
                </InfoHint>
              </th>
              <th className="text-end">
                Used
                <InfoHint alignEnd>
                  Number of deposit generations the key has gone through. <code>(wN)</code> = the
                  key&apos;s withdrawable epoch N after an exit.
                </InfoHint>
              </th>
              <th className="text-end">
                Bids / Won
                <InfoHint alignEnd>
                  Bids signed with this key / bids won, counted since buildoor started.
                </InfoHint>
              </th>
              {isLoggedIn && (
                <th className="text-end">
                  Actions
                  <InfoHint alignEnd>
                    <dl>
                      <dt>Deposit</dt>
                      <dd>register the key with a deposit of <code>deposit_amount</code></dd>
                      <dt>Top up</dt>
                      <dd>add another <code>deposit_amount</code> to its balance</dd>
                      <dt>Exit</dt>
                      <dd>initiate the builder exit (irreversible)</dd>
                    </dl>
                  </InfoHint>
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {keys.map((key) => {
              const badge = STATUS_BADGE[key.status] ?? STATUS_BADGE.unused;
              const busy = busyKey === key.key_index;
              const canExit = key.status === 'active' || key.status === 'pending';
              const canDeposit = key.status === 'unused' || key.status === 'withdrawn';
              const canTopup = key.status === 'active' || key.status === 'pending';

              return (
                <React.Fragment key={key.key_index}>
                  <tr>
                    <td>{key.key_index}</td>
                    <td className="font-monospace small">
                      <CopyableHash value={key.pubkey} chars={8} />
                    </td>
                    <td>
                      <span className={`badge ${badge.className}`}>{badge.label}</span>
                    </td>
                    <td className="text-end">
                      {key.has_builder_index ? key.builder_index : <span className="text-muted">—</span>}
                    </td>
                    <td className="text-end">{formatGwei(key.balance_gwei)}</td>
                    <td className="text-end text-warning">
                      {key.pending_payments_gwei > 0 ? `-${formatGwei(key.pending_payments_gwei)}` : '—'}
                    </td>
                    <td className="text-end fw-bold">{formatGwei(key.effective_balance_gwei)}</td>
                    <td className="text-end">
                      {key.use_count}
                      {key.withdrawable_epoch > 0 && key.withdrawable_epoch < FAR_FUTURE_EPOCH && (
                        <span className="text-warning ms-1 small" title="Withdrawable epoch">
                          (w{key.withdrawable_epoch})
                        </span>
                      )}
                    </td>
                    <td className="text-end small">
                      {key.bids_submitted} / {key.bids_won}
                    </td>
                    {isLoggedIn && (
                      <td className="text-end text-nowrap">
                        {canDeposit && (
                          <button
                            className="btn btn-sm btn-outline-primary py-0 px-1 me-1"
                            disabled={busy}
                            onClick={() => onDeposit(key.key_index)}
                          >
                            Deposit
                          </button>
                        )}
                        {canTopup && (
                          <button
                            className="btn btn-sm btn-outline-primary py-0 px-1 me-1"
                            disabled={busy}
                            onClick={() => onTopup(key.key_index)}
                          >
                            Top up
                          </button>
                        )}
                        {canExit && (
                          <button
                            className="btn btn-sm btn-outline-danger py-0 px-1"
                            disabled={busy}
                            onClick={() => {
                              setConfirmExit(key.key_index);
                              setLowerTarget(true);
                            }}
                          >
                            Exit
                          </button>
                        )}
                        {busy && <i className="fas fa-spinner fa-spin ms-1"></i>}
                      </td>
                    )}
                  </tr>
                  {confirmExit === key.key_index && (
                    <tr className="table-warning">
                      <td colSpan={isLoggedIn ? 10 : 9}>
                        <div className="small">
                          <strong>Exit builder key #{key.key_index}?</strong> This is irreversible —
                          the key cannot be reactivated until its registry entry is reused by another
                          builder&apos;s deposit.
                        </div>
                        <div className="d-flex align-items-center gap-3 mt-2">
                          <div className="form-check form-check-inline mb-0">
                            <input
                              className="form-check-input"
                              type="checkbox"
                              id={`lower-target-${key.key_index}`}
                              checked={lowerTarget}
                              onChange={(e) => setLowerTarget(e.target.checked)}
                            />
                            <label className="form-check-label small" htmlFor={`lower-target-${key.key_index}`}>
                              Lower the target to {Math.max(target - 1, 1)} (otherwise a replacement
                              key is deposited)
                            </label>
                          </div>
                          <button
                            className="btn btn-sm btn-danger ms-auto"
                            onClick={() => {
                              setConfirmExit(null);
                              onExit(key.key_index, lowerTarget);
                            }}
                          >
                            Confirm exit
                          </button>
                          <button
                            className="btn btn-sm btn-secondary"
                            onClick={() => setConfirmExit(null)}
                          >
                            Cancel
                          </button>
                        </div>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
};
