import React from 'react';
import { StatTile } from '../ui/StatTile';
import type { BuilderInfo, ServiceStatus, Stats, TxPoolStats } from '../../types';
import { count, gweiToEthText } from './format';

interface StatsRowProps {
  stats: Stats | null;
  builderInfo: BuilderInfo | null;
  serviceStatus: ServiceStatus | null;
  txPoolStats: TxPoolStats | null;
}

// StatsRow is the dashboard's headline: what the builder produced since it
// started, one tile per outcome.
const StatsRowView: React.FC<StatsRowProps> = ({ stats, builderInfo, serviceStatus, txPoolStats }) => {
  const pendingGwei = builderInfo?.total_pending_payments_gwei ?? builderInfo?.pending_payments_gwei ?? 0;
  const mempoolAvailable = serviceStatus?.txpool_available !== false;

  return (
    <section className="db-stats" aria-label="Statistics">
      <StatTile label="Payloads built" value={count(stats?.slots_built)} note={`${count(stats?.blocks_included)} included`} />
      <StatTile label="Bids submitted" value={count(stats?.bids_submitted)} note={`${count(stats?.bids_won)} won`} />
      <StatTile
        label="Payloads revealed"
        value={count(stats?.reveals_success)}
        note={`${count(stats?.reveals_skipped)} withheld · ${count(stats?.reveals_failed)} failed`}
      />
      <StatTile
        label="Paid to proposers"
        value={`${gweiToEthText(stats?.total_paid_gwei ?? 0)} ETH`}
        note={pendingGwei > 0 ? `${gweiToEthText(pendingGwei)} ETH pending` : 'ePBS bid payments'}
      />
      <StatTile
        label="Builder API headers"
        value={count(stats?.builder_api_headers_requested)}
        note={`${count(stats?.builder_api_blocks_published)} blocks published`}
      />
      <StatTile
        label="Registered validators"
        value={count(stats?.builder_api_registered_validators)}
        note="Builder API registrations"
      />
      <StatTile
        label="Builder balance"
        value={`${gweiToEthText(builderInfo?.total_balance_gwei ?? builderInfo?.cl_balance_gwei ?? 0, 2)} ETH`}
        note={builderInfo ? `${count(builderInfo.keys_active)} of ${count(builderInfo.key_target)} keys active` : '—'}
      />
      <StatTile
        label="Mempool"
        value={mempoolAvailable ? count(txPoolStats?.pending) : '—'}
        note={!mempoolAvailable ? 'not available' : serviceStatus?.txpool_enabled ? 'txs queued' : 'txs queued · paused'}
      />
    </section>
  );
};

export const StatsRow = React.memo(StatsRowView);
