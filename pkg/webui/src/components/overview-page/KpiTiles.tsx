import React from 'react';
import { StatTile } from '../ui/StatTile';
import type { Stats, TxPoolStats } from '../../types';

interface KpiTilesProps {
  stats: Stats | null;
  winsTotal: number | null;
  retentionEpochs?: number;
  mempoolOpen: boolean;
  txPoolStats: TxPoolStats | null;
}

function paidGwei(stats: Stats | null): number | null {
  return stats ? stats.total_paid_gwei : null;
}

function formatEth(gwei: number): string {
  const eth = gwei / 1e9;
  if (eth === 0) return '0 ETH';
  if (eth < 0.0001) return '< 0.0001 ETH';
  return `${eth.toLocaleString(undefined, { maximumFractionDigits: 4 })} ETH`;
}

export const KpiTiles: React.FC<KpiTilesProps> = ({ stats, winsTotal, retentionEpochs, mempoolOpen, txPoolStats }) => {
  const paid = paidGwei(stats);
  const blocksWon = winsTotal ?? stats?.blocks_included ?? null;

  return (
    <section className="ov-kpis" aria-label="Key figures">
      <StatTile
        label="Blocks won"
        value={blocksWon === null ? '—' : blocksWon.toLocaleString()}
        note={winsTotal !== null && retentionEpochs ? `last ${retentionEpochs} epochs` : 'included on chain'}
        title="Blocks built by buildoor that were included on chain (Builder API and ePBS)"
      />
      <StatTile
        label="Paid to proposers (ePBS)"
        value={paid === null ? '—' : formatEth(paid)}
        note="winning p2p bid payments"
        title="Sum of the bid values buildoor paid to proposers for won ePBS bids, as counted by this process"
      />
      <StatTile
        label="Payloads built"
        value={stats ? stats.slots_built.toLocaleString() : '—'}
        note="since the builder started"
      />
      <StatTile
        label="Private transactions queued"
        value={mempoolOpen && txPoolStats ? txPoolStats.pending.toLocaleString() : '—'}
        note={mempoolOpen ? 'waiting in the mempool' : 'mempool not available'}
      />
    </section>
  );
};
