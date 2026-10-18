import React from 'react';
import { StatTile } from '../ui/StatTile';
import { formatBytes, relativeTime, weiToEth } from './format';
import type { TxPoolStats } from '../../types';

interface MempoolStatsProps {
  stats: TxPoolStats | null;
  // Block gas limit of the current head (from the selection preview); 0 when
  // unknown, which hides the drain estimate.
  gasLimit: number;
}

const InlineStat: React.FC<{ label: string; value: React.ReactNode; title?: string }> = ({ label, value, title }) => (
  <span className="bd-mp-inline-stat" title={title}>
    <span className="bd-muted">{label}</span> <strong>{value}</strong>
  </span>
);

// MempoolStats shows the pool aggregates and lifetime counters: the five
// headline tiles and one compact line with the rest.
export const MempoolStats: React.FC<MempoolStatsProps> = ({ stats, gasLimit }) => {
  const rejected = Object.entries(stats?.rejected ?? {})
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1]);
  const rejectedTotal = rejected.reduce((sum, [, n]) => sum + n, 0);
  const rejectedNote = rejected.length > 0
    ? rejected.slice(0, 2).map(([reason, n]) => `${reason} ${n}`).join(' · ') + (rejected.length > 2 ? ' …' : '')
    : undefined;
  const rejectedTitle = rejected.length > 0
    ? `Refused at admission:\n${rejected.map(([reason, n]) => `${reason}: ${n}`).join('\n')}`
    : 'Refused at admission';

  const gasSum = stats?.gas_sum ?? 0;
  const drainNote = gasLimit > 0 ? `${(gasSum / gasLimit).toFixed(2)} blocks to drain` : undefined;

  return (
    <div className="d-flex flex-column gap-2">
      <div className="bd-mp-stats">
        <StatTile
          label="Pending"
          value={(stats?.pending ?? 0).toLocaleString()}
          note={`from ${(stats?.senders ?? 0).toLocaleString()} senders`}
          title="Queued transactions"
        />
        <StatTile
          label="Gas queued"
          value={gasSum.toLocaleString()}
          note={drainNote}
          title="Sum of the gas limits of all queued transactions (blocks to drain = gas sum / current block gas limit)"
        />
        <StatTile
          label="Included by us"
          value={(stats?.evicted_included_by_us ?? 0).toLocaleString()}
          title="Evicted because a block we built included them"
        />
        <StatTile
          label="Leaked"
          value={(stats?.evicted_included_by_other ?? 0).toLocaleString()}
          note="included by others"
          title="Included by a block we did not build (forward mode or duplicate submission elsewhere)"
        />
        <StatTile label="Rejected" value={rejectedTotal.toLocaleString()} note={rejectedNote} title={rejectedTitle} />
      </div>
      <div className="bd-mp-inline-stats small">
        <InlineStat label="Admitted" value={(stats?.admitted ?? 0).toLocaleString()} title="Lifetime admissions" />
        <InlineStat label="Value" value={`${weiToEth(stats?.value_wei)} ETH`} title="Summed value of the queued transactions" />
        <InlineStat label="Size" value={formatBytes(stats?.bytes ?? 0)} />
        <InlineStat label="Blob txs / blobs" value={`${stats?.blob_txs ?? 0} / ${stats?.blobs ?? 0}`} />
        <InlineStat label="Nonce too low" value={(stats?.evicted_nonce_too_low ?? 0).toLocaleString()} title="Evicted because the chain passed their nonce" />
        <InlineStat label="Expired" value={(stats?.evicted_ttl ?? 0).toLocaleString()} title="Evicted by the slot TTL" />
        <InlineStat label="Struck out" value={(stats?.evicted_strikes ?? 0).toLocaleString()} title="Dropped after repeated attributed EL refusals" />
        <InlineStat
          label="Last admission"
          value={stats?.last_admitted_at ? relativeTime(stats.last_admitted_at) : '—'}
          title={stats?.last_admitted_at}
        />
      </div>
    </div>
  );
};
