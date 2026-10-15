import React from 'react';
import { setView } from '../../stores/viewStore';
import type { BuilderInfo, ServiceStatus } from '../../types';
import { CopyableHash } from '../CopyableHash';
import { InfoHint } from '../InfoHint';
import { Tag, type Tone } from '../ui/Tag';
import { count, gweiToEthText, weiToEthText } from './format';

interface BuilderCardProps {
  builderInfo: BuilderInfo | null;
  serviceStatus: ServiceStatus | null;
}

function registration(state: string | undefined, index: number): { tone: Tone; text: string } {
  switch (state) {
    case 'registered':
      return { tone: 'ok', text: `#${index}` };
    case 'pending_finalization':
      return { tone: 'info', text: `#${index} · pending finalization` };
    case 'exiting':
      return { tone: 'warn', text: `#${index} · exiting` };
    case 'exited':
      return { tone: 'neutral', text: `#${index} · exited` };
    case 'waiting_gloas':
      return { tone: 'info', text: 'awaiting Gloas' };
    case 'pending':
      return { tone: 'warn', text: 'registering…' };
    case 'unregistered':
      return { tone: 'neutral', text: 'unregistered' };
    default:
      return { tone: 'neutral', text: 'unknown' };
  }
}

// BuilderCard shows the builder's identity and money: entry key, registry
// index, funding wallet and the fleet's balances.
export const BuilderCard: React.FC<BuilderCardProps> = ({ builderInfo, serviceStatus }) => {
  const state = serviceStatus?.epbs_registration_state;
  const reg = registration(state, builderInfo?.builder_index ?? 0);
  const fleet = (builderInfo?.key_target ?? 1) > 1 || (builderInfo?.key_count ?? 1) > 1;
  const pending = builderInfo
    ? fleet
      ? builderInfo.total_pending_payments_gwei
      : builderInfo.pending_payments_gwei
    : 0;

  return (
    <section className="card db-card" aria-labelledby="db-builder-title">
      <div className="db-card-head">
        <h2 className="bd-card-title" id="db-builder-title">
          Builder
        </h2>
        {builderInfo && (
          <Tag tone={builderInfo.keys_active > 0 ? 'ok' : 'neutral'}>
            {count(builderInfo.keys_active)} / {count(builderInfo.key_target)} keys active
          </Tag>
        )}
        <button type="button" className="bd-btn bd-btn-sm ms-auto" onClick={() => setView('builder-keys')}>
          Keys
        </button>
      </div>

      {(state === 'exiting' || state === 'exited') && (
        <div className="db-alert">
          Builder key {state === 'exited' ? 'exited' : 'exiting'}: it cannot be reactivated. Deposits to it only
          top up the exited entry and are withdrawn back to the wallet, so top-ups are disabled.
        </div>
      )}

      {!builderInfo ? (
        <p className="bd-muted small mb-0">Waiting for builder info…</p>
      ) : (
        <>
          <dl className="bd-kv">
            <dt>
              Entry pubkey
              <InfoHint>
                BLS public key of the primary builder key: the entry key from <code>--builder-privkey</code> or
                the mnemonic (internal index 0). Other managed keys are listed on the Builder keys page.
              </InfoHint>
            </dt>
            <dd>
              <CopyableHash value={builderInfo.builder_pubkey} chars={6} />
            </dd>
            <dt>
              Registry index
              <InfoHint>
                The primary key&apos;s builder index in the beacon state registry, assigned at deposit and
                reused by other builders after an exit, with its registration state.
              </InfoHint>
            </dt>
            <dd>
              <Tag tone={reg.tone}>{reg.text}</Tag>
            </dd>
            {builderInfo.wallet_address && (
              <>
                <dt>Wallet</dt>
                <dd>
                  <CopyableHash value={builderInfo.wallet_address} chars={6} />
                </dd>
              </>
            )}
            {builderInfo.builder_index > 0 && builderInfo.deposit_epoch > 0 && (
              <>
                <dt>Deposit epoch</dt>
                <dd>{count(builderInfo.deposit_epoch)}</dd>
              </>
            )}
            {builderInfo.withdrawable_epoch > 0 && builderInfo.withdrawable_epoch < 2 ** 53 && (
              <>
                <dt>Withdrawable</dt>
                <dd>epoch {count(builderInfo.withdrawable_epoch)}</dd>
              </>
            )}
          </dl>
          <div className="db-money">
            <div>
              <div className="bd-stat-label">CL balance</div>
              <div className="db-money-value">
                {gweiToEthText(fleet ? builderInfo.total_balance_gwei : builderInfo.cl_balance_gwei, 2)}
              </div>
              <div className="bd-stat-note">ETH{fleet ? `, ${count(builderInfo.key_count)} keys` : ''}</div>
            </div>
            <div>
              <div className="bd-stat-label">Pending</div>
              <div className="db-money-value">{gweiToEthText(pending)}</div>
              <div className="bd-stat-note">ETH payments</div>
            </div>
            <div>
              <div className="bd-stat-label">Wallet</div>
              <div className="db-money-value">{weiToEthText(builderInfo.wallet_balance_wei)}</div>
              <div className="bd-stat-note">ETH</div>
            </div>
          </div>
        </>
      )}
    </section>
  );
};
