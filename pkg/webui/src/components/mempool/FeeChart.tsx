import React, { useEffect, useMemo, useRef, useState } from 'react';
import type { FeeDistribution } from '../../types';

const HEIGHT = 200;
const PAD = { left: 48, right: 48, top: 14, bottom: 30 };
const BUCKETS_PER_DECADE = 8;
// The fee axis starts this many buckets (1.5 decades) below the base fee;
// cheaper transactions share one "below" bar. A busy testnet mempool parks
// most of its gas far below the base fee, which would flatten the rest.
const BUCKETS_BELOW_BASE = 12;

function gwei(wei: string): number {
  try {
    return Number(BigInt(wei)) / 1e9;
  } catch {
    return 0;
  }
}

function formatGas(gas: number): string {
  if (gas >= 1e9) return `${(gas / 1e9).toFixed(1)}G`;
  if (gas >= 1e6) return `${(gas / 1e6).toFixed(gas >= 1e7 ? 0 : 1)}M`;
  if (gas >= 1e3) return `${(gas / 1e3).toFixed(0)}k`;
  return `${gas}`;
}

function formatFee(value: number): string {
  if (value >= 100) return value.toFixed(0);
  if (value >= 1) return value.toFixed(1);
  return value.toPrecision(2);
}

interface Bar {
  lo: number; // gwei
  hi: number;
  elGas: number;
  elTxs: number;
  privateGas: number;
  privateTxs: number;
  below: boolean; // the lumped bar of everything under the visible range
}

// useWidth tracks an element's rendered width, so the chart draws in real
// pixels at a fixed height instead of scaling text with the page.
function useWidth(): [React.RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement | null>(null);
  const [width, setWidth] = useState(0);

  useEffect(() => {
    if (!ref.current) return undefined;
    const observer = new ResizeObserver((entries) => setWidth(Math.floor(entries[0].contentRect.width)));
    observer.observe(ref.current);
    return () => observer.disconnect();
  }, []);

  return [ref, width];
}

interface FeeChartProps {
  fees: FeeDistribution;
}

// FeeChart draws the pending gas by max fee per gas: one stacked bar per
// log-scale fee bucket (EL mempool, buildoor's private transactions on top),
// the gas at or above each fee (line, right axis), the next base fee
// (transactions left of it cannot be included) and one block of gas.
export const FeeChart: React.FC<FeeChartProps> = ({ fees }) => {
  const [ref, width] = useWidth();
  const baseFee = gwei(fees.next_base_fee);

  const model = useMemo(() => {
    const all = fees.buckets.map((b) => ({
      lo: gwei(b.min_fee),
      hi: gwei(b.max_fee),
      elGas: b.el_gas,
      elTxs: b.el_txs,
      privateGas: b.private_gas ?? 0,
      privateTxs: b.private_txs ?? 0,
      below: false,
    }));
    if (all.length === 0) return null;

    const floor = baseFee > 0 ? baseFee / Math.pow(10, BUCKETS_BELOW_BASE / BUCKETS_PER_DECADE) : 0;
    const bars: Bar[] = [];
    let below: Bar | null = null;

    for (const b of all) {
      if (b.hi <= floor) {
        below ??= { ...b, elGas: 0, elTxs: 0, privateGas: 0, privateTxs: 0, below: true };
        below.hi = b.hi;
        below.elGas += b.elGas;
        below.elTxs += b.elTxs;
        below.privateGas += b.privateGas;
        below.privateTxs += b.privateTxs;
      } else {
        bars.push(b);
      }
    }
    if (below) bars.unshift(below);

    // Gas at or above each bar's lower edge.
    const cumulative: number[] = new Array(bars.length).fill(0);
    let running = 0;
    for (let i = bars.length - 1; i >= 0; i--) {
      running += bars[i].elGas + bars[i].privateGas;
      cumulative[i] = running;
    }

    const firstAbove = bars.findIndex((b) => b.lo >= baseFee);
    const includable = firstAbove >= 0 ? cumulative[firstAbove] : 0;

    // The bars scale to what is near or above the base fee, the part a block
    // can take; the cheaper bars clip with their value printed.
    const relevant = bars.filter((b) => !b.below && b.hi > baseFee / 2).map((b) => b.elGas + b.privateGas);
    const fallback = bars.filter((b) => !b.below).map((b) => b.elGas + b.privateGas);
    const maxBar = Math.max(1, ...(relevant.length > 0 ? relevant : fallback)) * 1.1;

    return { bars, cumulative, includable, maxBar };
  }, [fees, baseFee]);

  if (!model) {
    return <div className="text-center py-4 bd-muted small">No pending transactions</div>;
  }

  const { bars, cumulative, includable, maxBar } = model;
  const gasLimit = fees.gas_limit || 0;
  // The cumulative axis covers the gas that clears the base fee, at least two
  // blocks, so the block line stays readable; the cheaper part runs off it.
  const cumMax = Math.max(includable * 1.15, 2 * gasLimit, 1);

  const plotW = Math.max(width - PAD.left - PAD.right, 1);
  const plotH = HEIGHT - PAD.top - PAD.bottom;
  const barW = plotW / bars.length;
  const bottom = PAD.top + plotH;

  const yBar = (gas: number): number => bottom - Math.min(gas / maxBar, 1) * plotH;
  const yCum = (gas: number): number => bottom - Math.min(gas / cumMax, 1) * plotH;

  const baseIndex = bars.findIndex((b) => b.hi > baseFee);
  const baseX = baseIndex < 0
    ? PAD.left + plotW
    : PAD.left + (baseIndex + Math.max(0, Math.min(1,
      Math.log10(baseFee / bars[baseIndex].lo) / Math.log10(bars[baseIndex].hi / bars[baseIndex].lo)))) * barW;

  const cumPath = cumulative
    .map((gas, i) => `${i === 0 ? 'M' : 'L'}${(PAD.left + i * barW).toFixed(1)},${yCum(gas).toFixed(1)} h${barW.toFixed(1)}`)
    .join(' ');

  const labelEvery = Math.max(1, Math.ceil(56 / barW));

  return (
    <div className="bd-mp-feechart" ref={ref}>
      {width > 0 && (
        <svg width={width} height={HEIGHT} role="img" aria-label="Pending gas by max fee per gas">
          <rect className="bd-mp-feechart-excluded" x={PAD.left} y={PAD.top} width={Math.max(baseX - PAD.left, 0)} height={plotH} />

          {bars.map((b, i) => {
            const x = PAD.left + i * barW + 1;
            const w = Math.max(barW - 2, 1);
            const total = b.elGas + b.privateGas;
            const elTop = yBar(b.elGas);
            const totalTop = yBar(total);
            const clipped = total > maxBar;
            return (
              <g key={`${b.lo}-${b.below}`}>
                <title>
                  {`${b.below ? `below ${formatFee(b.hi)}` : `${formatFee(b.lo)}–${formatFee(b.hi)}`} gwei\n` +
                    `EL mempool: ${b.elTxs.toLocaleString()} txs, ${formatGas(b.elGas)} gas\n` +
                    (fees.private ? `buildoor private: ${b.privateTxs} txs, ${formatGas(b.privateGas)} gas\n` : '') +
                    `at or above this fee: ${formatGas(cumulative[i])} gas` +
                    (gasLimit ? ` (${(cumulative[i] / gasLimit).toFixed(1)} blocks)` : '')}
                </title>
                <rect className={`bd-mp-feechart-el${b.below ? ' bd-mp-feechart-below' : ''}`} x={x} y={elTop} width={w} height={bottom - elTop} />
                {b.privateGas > 0 && (
                  <rect className="bd-mp-feechart-private" x={x} y={totalTop} width={w} height={Math.max(elTop - totalTop, 2)} />
                )}
                {clipped && (
                  <text className="bd-mp-feechart-label" x={x + w / 2} y={PAD.top - 3} textAnchor="middle">{formatGas(total)}</text>
                )}
                <rect x={x - 1} y={PAD.top} width={barW} height={plotH} fill="transparent" />
              </g>
            );
          })}

          <path className="bd-mp-feechart-cum" d={cumPath} />
          {gasLimit > 0 && (
            <g>
              <line className="bd-mp-feechart-block" x1={PAD.left} x2={PAD.left + plotW} y1={yCum(gasLimit)} y2={yCum(gasLimit)} />
              <text className="bd-mp-feechart-label" x={PAD.left + plotW - 4} y={yCum(gasLimit) - 4} textAnchor="end">1 block</text>
            </g>
          )}

          <line className="bd-mp-feechart-base" x1={baseX} x2={baseX} y1={PAD.top} y2={bottom} />
          <text className="bd-mp-feechart-label bd-mp-feechart-base-label" x={baseX + 4} y={PAD.top + 10}>
            base fee {formatFee(baseFee)} gwei
          </text>

          <line className="bd-mp-feechart-axis" x1={PAD.left} x2={PAD.left + plotW} y1={bottom} y2={bottom} />
          {bars.map((b, i) => (i % labelEvery === 0 ? (
            <text key={`tick-${i}`} className="bd-mp-feechart-label" x={PAD.left + i * barW + barW / 2} y={bottom + 13} textAnchor="middle">
              {b.below ? `<${formatFee(b.hi)}` : formatFee(b.lo)}
            </text>
          ) : null))}
          <text className="bd-mp-feechart-label" x={PAD.left + plotW / 2} y={HEIGHT - 2} textAnchor="middle">max fee per gas (gwei)</text>
          <text className="bd-mp-feechart-label" x={PAD.left - 6} y={PAD.top + 8} textAnchor="end">{formatGas(maxBar)}</text>
          <text className="bd-mp-feechart-label" x={PAD.left - 6} y={bottom} textAnchor="end">0</text>
          <text className="bd-mp-feechart-label bd-mp-feechart-cum-label" x={PAD.left + plotW + 6} y={PAD.top + 8}>{formatGas(cumMax)}</text>
          <text className="bd-mp-feechart-label bd-mp-feechart-cum-label" x={PAD.left + plotW + 6} y={bottom}>0</text>
        </svg>
      )}
      <div className="bd-mp-feechart-legend small">
        <span><i className="bd-mp-feechart-swatch"></i>EL mempool gas per fee step</span>
        {fees.private && <span><i className="bd-mp-feechart-swatch bd-mp-feechart-private"></i>buildoor private</span>}
        <span><i className="bd-mp-feechart-swatch-line"></i>gas at or above the fee (right axis)</span>
        <span>
          <strong>{formatGas(includable)}</strong> gas clears the base fee
          {gasLimit ? ` (${(includable / gasLimit).toFixed(1)} blocks)` : ''}
        </span>
      </div>
    </div>
  );
};
