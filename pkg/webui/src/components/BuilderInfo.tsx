import React, { useEffect, useState } from 'react';
import { useAuthContext } from '../context/AuthContext';
import type { BuilderInfo as BuilderInfoType, ServiceStatus, Config } from '../types';
import { CopyableHash } from './CopyableHash';
import { InfoHint } from './InfoHint';
import { useBuilderKeyActions } from '../hooks/useBuilderKeys';
import { useSettings } from '../hooks/useSettings';
import { setView } from '../stores/viewStore';

// Explanations shared by the view rows and the lifecycle edit form.
const HINT_LIFECYCLE = (
  <>
    Lifecycle management (<code>lifecycle_enabled</code>): keeps the managed key set at the target
    count by depositing, topping up and exiting builder keys from the funding wallet, one
    transaction per pass. Paused = no automatic lifecycle transactions. Only available when
    started with <code>--el-rpc</code> and <code>--wallet-privkey</code>.
  </>
);

const HINT_TARGET_KEYS = (
  <>
    Number of builder keys kept registered and funded (<code>builder_keys.target_count</code>),
    derived from the entry key (index 0 is the entry key itself; 1 = single-key setup). Raising it
    deposits new keys; lowering it exits surplus keys, highest index first, when auto exit is on.
    Capped by the derivation limit (<code>builder_keys.max_index</code> + 1).
  </>
);

const HINT_DEPOSIT_AMOUNT = (
  <>
    The one amount sent with every builder key deposit (<code>deposit_amount</code>): early
    pre-Gloas onboarding, the registering builder deposit and each top-up. Must be greater than 0.
  </>
);

const HINT_TOPUP_THRESHOLD = (
  <>
    A registered key whose effective balance (balance minus pending payments) drops below this is
    topped up by the deposit amount (<code>topup_threshold</code>). Exiting or exited keys are
    never topped up.
  </>
);

const HINT_MAX_DEPOSIT_FEE = (
  <>
    Upper bound in gwei for the builder deposit contract&apos;s queue fee
    (<code>deposit_max_fee</code>). Deposits and top-ups wait while the current fee is above it,
    which also slows a fleet ramp-up when the queue grows. 0 = no limit.
  </>
);

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
  const [lcMaxFee, setLcMaxFee] = useState('');
  const [lcError, setLcError] = useState('');
  const { postSettings } = useSettings();
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
    setLcAmount(config ? String(config.deposit_amount / 1e9) : '');
    setLcMaxFee(config ? String(config.deposit_max_fee) : '');
    setLcError('');
    setEditingLifecycle(true);
  };

  const handleLifecycleSave = async () => {
    if (!isLoggedIn) return;

    const amount = Math.round(parseFloat(lcAmount) * 1e9);
    const threshold = Math.round(parseFloat(lcThreshold) * 1e9);
    const maxFee = parseInt(lcMaxFee, 10);
    if (!Number.isFinite(amount) || !Number.isFinite(threshold) || !Number.isFinite(maxFee)) {
      setLcError('all three values are required');
      return;
    }

    const result = await postSettings({
      'deposit_amount': amount,
      'topup_threshold': threshold,
      'deposit_max_fee': maxFee,
    });
    if (!result.ok) {
      setLcError(result.error ?? 'failed to update lifecycle config');
      return;
    }

    setLcError('');
    setEditingLifecycle(false);
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
            <span className="ms-1"><InfoHint>{HINT_LIFECYCLE}</InfoHint></span>
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
          <>
            <span className="badge bg-dark">No Lifecycle</span>
            <span className="ms-1"><InfoHint>{HINT_LIFECYCLE}</InfoHint></span>
          </>
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
              <td className="text-muted">
                Pubkey:{' '}
                <InfoHint>
                  BLS public key of the primary builder key: the entry key from{' '}
                  <code>--builder-privkey</code> or the mnemonic (internal index 0). Other managed
                  keys are listed on the Builder Keys page.
                </InfoHint>
              </td>
              <td className="text-end font-monospace small">
                <CopyableHash value={builderInfo.builder_pubkey} chars={8} />
              </td>
            </tr>
            <tr>
              <td className="text-muted">
                Index:{' '}
                <InfoHint>
                  The primary key&apos;s builder index in the beacon state registry, assigned at
                  deposit and reused by other builders after an exit. The badge shows the
                  registration state: registered, pending finalization, exiting, exited, awaiting
                  Gloas, registering or unregistered.
                </InfoHint>
              </td>
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
                <td className="text-muted">
                  Target keys: <InfoHint>{HINT_TARGET_KEYS}</InfoHint>
                </td>
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

            {/* Wallet Info */}
            {builderInfo.wallet_address && (
              <tr>
                <td className="text-muted">
                  Wallet:{' '}
                  <InfoHint>
                    Funding wallet (<code>--wallet-privkey</code>) that pays every deposit, top-up
                    and exit transaction.
                  </InfoHint>
                </td>
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
              <td className="text-muted">
                {isFleet ? 'CL Balance (all keys):' : 'CL Balance:'}{' '}
                <InfoHint>
                  Builder balance on the consensus layer from the latest epoch state, adjusted live
                  for top-ups and revealed bid payments since then. Summed over all managed keys when
                  more than one is managed.
                </InfoHint>
              </td>
              <td className="text-end">
                <span className="text-primary fw-bold">
                  {formatGwei(isFleet ? builderInfo.total_balance_gwei : builderInfo.cl_balance_gwei)} ETH
                </span>
              </td>
            </tr>

            {/* Pending Payments (always shown) */}
            <tr>
              <td className="text-muted">
                Pending Payments:{' '}
                <InfoHint>
                  Bid payments the beacon state still owes to proposers for won slots; they are
                  deducted from the balance once they settle.
                </InfoHint>
              </td>
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
                <td className="text-muted">
                  Effective Balance:{' '}
                  <InfoHint>
                    CL balance minus pending payments: what is actually left to back new bids.
                  </InfoHint>
                </td>
                <td className="text-end text-success fw-bold">
                  {formatGwei(
                    isFleet ? builderInfo.total_effective_gwei : builderInfo.effective_balance_gwei,
                  )} ETH
                </td>
              </tr>
            )}

            {/* Wallet Balance */}
            {builderInfo.wallet_balance_wei && (
              <>
                <tr>
                  <td colSpan={2}><hr className="my-1" /></td>
                </tr>
                <tr>
                  <td className="text-muted">
                    Wallet Balance:{' '}
                    <InfoHint>Execution-layer balance of the funding wallet.</InfoHint>
                  </td>
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
                  <td className="text-muted small">
                    Deposit Epoch:{' '}
                    <InfoHint>Epoch the primary key&apos;s builder deposit was processed.</InfoHint>
                  </td>
                  <td className="text-end small">{builderInfo.deposit_epoch}</td>
                </tr>
                {builderInfo.withdrawable_epoch > 0 && builderInfo.withdrawable_epoch < 18446744073709551615 && (
                  <tr>
                    <td className="text-muted small text-warning">
                      Withdrawable:{' '}
                      <InfoHint>
                        Epoch from which the exited primary key&apos;s balance can be withdrawn
                        back to the wallet.
                      </InfoHint>
                    </td>
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
                      <td
                        className="text-muted small"
                        title="Sent with every builder key deposit: early onboarding, registration and each top-up"
                      >
                        Deposit Amount: <InfoHint>{HINT_DEPOSIT_AMOUNT}</InfoHint>
                      </td>
                      <td className="text-end small">
                        {formatGwei(config.deposit_amount)} ETH
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
                      <td className="text-muted small">
                        Topup Threshold: <InfoHint>{HINT_TOPUP_THRESHOLD}</InfoHint>
                      </td>
                      <td className="text-end small">{formatGwei(config.topup_threshold)} ETH</td>
                    </tr>
                    <tr>
                      <td className="text-muted small">
                        Max Deposit Fee: <InfoHint>{HINT_MAX_DEPOSIT_FEE}</InfoHint>
                      </td>
                      <td className="text-end small">
                        {config.deposit_max_fee > 0
                          ? `${config.deposit_max_fee.toLocaleString()} gwei`
                          : 'no limit'}
                      </td>
                    </tr>
                  </>
                ) : (
                  <>
                    <tr>
                      <td className="text-muted small">
                        Deposit Amount (ETH): <InfoHint>{HINT_DEPOSIT_AMOUNT}</InfoHint>
                      </td>
                      <td className="text-end">
                        <input
                          type="number"
                          step="0.1"
                          min={0}
                          className="form-control form-control-sm"
                          style={{ fontSize: '11px' }}
                          value={lcAmount}
                          onChange={(e) => setLcAmount(e.target.value)}
                        />
                      </td>
                    </tr>
                    <tr>
                      <td colSpan={2} className="form-text mt-0">
                        Sent with every builder key deposit — early onboarding, registration
                        and each top-up.
                      </td>
                    </tr>
                    <tr>
                      <td className="text-muted small">
                        Topup Threshold (ETH): <InfoHint>{HINT_TOPUP_THRESHOLD}</InfoHint>
                      </td>
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
                      <td className="text-muted small">
                        Max Deposit Fee (gwei): <InfoHint>{HINT_MAX_DEPOSIT_FEE}</InfoHint>
                      </td>
                      <td className="text-end">
                        <input
                          type="number"
                          min={0}
                          className="form-control form-control-sm"
                          style={{ fontSize: '11px' }}
                          value={lcMaxFee}
                          onChange={(e) => setLcMaxFee(e.target.value)}
                        />
                      </td>
                    </tr>
                    <tr>
                      <td colSpan={2} className="form-text mt-0">
                        Deposits and top-ups wait while the deposit queue fee is above this
                        (0 = no limit).
                      </td>
                    </tr>
                    {lcError && (
                      <tr>
                        <td colSpan={2} className="small text-danger">{lcError}</td>
                      </tr>
                    )}
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
