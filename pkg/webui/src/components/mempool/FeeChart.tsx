import React, { useMemo } from 'react';
import type { FeeDistribution } from '../../types';

const WIDTH = 760;
const HEIGHT = 240;
const PAD = { left: 56, right: 56, top: 12, bottom: 34 };
const BUCKETS_PER_DECADE = 8;

function gwei(wei: string): number {
  try {
    return Number(BigInt(wei)) / 1e9;
  } catch {
    return 0;
  }
}

function formatGas(gas: number): string {
  if (gas >= 1e6) return `${(gas / 1e6).toFixed(gas >= 1e7 ? 0 : 1)}M`;
  if (gas >= 1e3) return `${(gas / 1e3).toFixed(0)}k`;
  return `${gas}`;
}

function formatFee(value: number): string {
  if (value >= 100) return value.toFixed(0);
  if (value >= 1) return value.toFixed(1);
  return value.toPrecision(2);
}

interface FeeChartProps {
  fees: FeeDistribution;
}

// FeeChart draws the pending gas by max fee per gas: one stacked bar per
// log-scale fee bucket (EL mempool, buildoor's private transactions on
// top), the cumulative gas at or above each fee (right axis, in blocks), the
// next base fee (transactions left of it cannot be included) and one block
// of gas on the cumulative scale.
export const FeeChart: React.FC<FeeChartProps> = ({ fees }) => {
  const model = useMemo(() => {
    const buckets = fees.buckets.map((b) => ({
      ...b,
      lo: gwei(b.min_fee),
      hi: gwei(b.max_fee),
      total: b.el_gas + (b.private_gas ?? 0),
    }));

    // Cumulative gas at or above each bucket's lower edge, from the top.
    const cumulative: number[] = new Array(buckets.length).fill(0);
    let running = 0;
    for (let i = buckets.length - 1; i >= 0; i--) {
      running += buckets[i].total;
      cumulative[i] = running;
    }

    return { buckets, cumulative, maxBar: Math.max(1, ...buckets.map((b) => b.total)), maxCum: Math.max(1, running) };
  }, [fees]);

  const { buckets, cumulative, maxBar } = model;
  if (buckets.length === 0) {
    return <div className="text-center py-4 bd-muted small">No pending transactions</div>;
  }

  const plotW = WIDTH - PAD.left - PAD.right;
  const plotH = HEIGHT - PAD.top - PAD.bottom;
  const barW = plotW / buckets.length;
  const gasLimit = fees.gas_limit || 0;
  // The cumulative scale shows at least one block, so the block line fits.
  const cumMax = Math.max(model.maxCum, gasLimit);

  const xOfFee = (fee: number): number => {
    const first = buckets[0].lo;
    const pos = (BUCKETS_PER_DECADE * Math.log10(Math.max(fee, 1e-9) / first)) * barW;
    return PAD.left + Math.min(Math.max(pos, 0), plotW);
  };
  const yBar = (gas: number): number => PAD.top + plotH - (gas / maxBar) * plotH;
  const yCum = (gas: number): number => PAD.top + plotH - (gas / cumMax) * plotH;

  const baseFee = gwei(fees.next_base_fee);
  const baseX = xOfFee(baseFee);

  const cumPath = cumulative
    .map((gas, i) => `${i === 0 ? 'M' : 'L'}${(PAD.left + i * barW).toFixed(1)},${yCum(gas).toFixed(1)} h${barW.toFixed(1)}`)
    .join(' ');

  // One tick per decade on the fee axis.
  const ticks = buckets
    .map((b, i) => ({ i, fee: b.lo }))
    .filter(({ fee }) => Math.abs(Math.log10(fee) - Math.round(Math.log10(fee))) < 1e-6);

  const includable = cumulative[buckets.findIndex((b) => b.lo >= baseFee)] ?? 0;

  return (
    <div className="bd-mp-feechart">
      <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label="Pending gas by max fee per gas">
        {/* Bars: EL mempool, private on top. */}
        {buckets.map((b, i) => {
          const x = PAD.left + i * barW;
          const privateGas = b.private_gas ?? 0;
          const elTop = yBar(b.el_gas);
          const totalTop = yBar(b.total);
          return (
            <g key={b.min_fee}>
              <title>
                {`${formatFee(b.lo)}–${formatFee(b.hi)} gwei\n` +
                  `EL mempool: ${b.el_txs} txs, ${formatGas(b.el_gas)} gas\n` +
                  (fees.private ? `buildoor private: ${b.private_txs ?? 0} txs, ${formatGas(privateGas)} gas\n` : '') +
                  `at or above this fee: ${formatGas(cumulative[i])} gas` +
                  (gasLimit ? ` (${(cumulative[i] / gasLimit).toFixed(2)} blocks)` : '')}
              </title>
              <rect className="bd-mp-feechart-el" x={x + 0.5} y={elTop} width={Math.max(barW - 1, 1)} height={PAD.top + plotH - elTop} />
              {privateGas > 0 && (
                <rect className="bd-mp-feechart-private" x={x + 0.5} y={totalTop} width={Math.max(barW - 1, 1)} height={elTop - totalTop} />
              )}
              <rect x={x} y={PAD.top} width={barW} height={plotH} fill="transparent" />
            </g>
          );
        })}

        {/* Cumulative gas at or above each fee, and one block of gas. */}
        <path className="bd-mp-feechart-cum" d={cumPath} />
        {gasLimit > 0 && (
          <g>
            <line className="bd-mp-feechart-block" x1={PAD.left} x2={PAD.left + plotW} y1={yCum(gasLimit)} y2={yCum(gasLimit)} />
            <text className="bd-mp-feechart-label" x={PAD.left + plotW - 4} y={yCum(gasLimit) - 4} textAnchor="end">1 block</text>
          </g>
        )}

        {/* Next base fee: what lies left of it cannot be included. */}
        <rect className="bd-mp-feechart-excluded" x={PAD.left} y={PAD.top} width={Math.max(baseX - PAD.left, 0)} height={plotH} />
        <line className="bd-mp-feechart-base" x1={baseX} x2={baseX} y1={PAD.top} y2={PAD.top + plotH} />
        <text className="bd-mp-feechart-label" x={baseX + 4} y={PAD.top + 12}>base fee {formatFee(baseFee)} gwei</text>

        {/* Axes. */}
        <line className="bd-mp-feechart-axis" x1={PAD.left} x2={PAD.left + plotW} y1={PAD.top + plotH} y2={PAD.top + plotH} />
        {ticks.map(({ i, fee }) => (
          <text key={i} className="bd-mp-feechart-label" x={PAD.left + i * barW} y={HEIGHT - PAD.bottom + 14} textAnchor="middle">
            {formatFee(fee)}
          </text>
        ))}
        <text className="bd-mp-feechart-label" x={PAD.left + plotW / 2} y={HEIGHT - 4} textAnchor="middle">max fee per gas (gwei)</text>
        <text className="bd-mp-feechart-label" x={PAD.left - 6} y={PAD.top + 8} textAnchor="end">{formatGas(maxBar)}</text>
        <text className="bd-mp-feechart-label" x={PAD.left - 6} y={PAD.top + plotH} textAnchor="end">0</text>
        <text className="bd-mp-feechart-label" x={PAD.left + plotW + 6} y={PAD.top + 8}>{formatGas(cumMax)}</text>
        <text className="bd-mp-feechart-label" x={PAD.left + plotW + 6} y={PAD.top + plotH}>0</text>
      </svg>
      <div className="bd-mp-feechart-legend small">
        <span><i className="bd-mp-feechart-swatch bd-mp-feechart-el"></i>EL mempool gas per fee bucket</span>
        {fees.private && <span><i className="bd-mp-feechart-swatch bd-mp-feechart-private"></i>buildoor private</span>}
        <span><i className="bd-mp-feechart-swatch-line bd-mp-feechart-cum"></i>gas at or above the fee (right axis)</span>
        <span className="bd-muted">
          {formatGas(includable)} gas clears the base fee
          {gasLimit ? ` (${(includable / gasLimit).toFixed(2)} blocks)` : ''}
        </span>
      </div>
    </div>
  );
};
