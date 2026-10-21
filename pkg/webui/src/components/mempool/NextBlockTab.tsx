import React, { useEffect, useState } from 'react';
import { Tag } from '../ui/Tag';
import { StatTile } from '../ui/StatTile';
import { CopyableHash } from '../CopyableHash';
import { buildLabel, lastSelection, primaryTarget } from '../../buildTargets';
import { weiToEth, weiToGwei } from './format';
import { useAuth } from '../../hooks/useAuth';
import { apiPath } from '../../utils/api';
import type {
  NextBlockEstimates,
  NextBlockResponse,
  NextBlockTx,
  PayloadInfo,
  PayloadTransactionsResponse,
  SelectionOrigins,
  SlotState,
  TxSelectionSummary,
} from '../../types';

// Every block on this tab — an estimate, our candidate, our included payload
// or another builder's block — is turned into one BlockModel and drawn by
// the one BlockView, row for row the same, so a card never changes its
// layout when what it shows changes.

type Origin = NextBlockTx['origin'];

const ORIGINS: Origin[] = ['inclusion_list', 'private', 'public'];

const ORIGIN_LABELS: Record<Origin, string> = {
  inclusion_list: 'Inclusion list',
  private: 'Private',
  public: 'Public',
};

const TX_SOURCE_LABELS: Record<string, string> = {
  txpool: 'private only',
  private_first: 'private first',
  most_profitable: 'most profitable',
  el_mempool: 'EL mempool',
  empty: 'empty',
};

const TX_LIST_ROWS = 15;

interface Segment {
  origin: Origin;
  txs: number;
  gas: number;
  blobs: number;
}

// BlockModel is what a block view draws.
interface BlockModel {
  kind: 'included' | 'candidate' | 'estimate';
  tags: React.ReactNode;
  // One line saying what the block is.
  description: React.ReactNode;
  gasLimit: number;
  gas: number; // used (built blocks) or reserved (estimates)
  gasLabel: 'used' | 'reserved';
  segments: Segment[];
  maxBlobs: number;
  value?: string; // wei
  valueNote?: string;
  txCount?: number;
  blobs: number;
  baseFee?: string; // wei
  selection?: TxSelectionSummary;
  txs?: NextBlockTx[];
  txsTotal?: number;
  txsStale?: boolean;
  // Why the transactions are not shown (while txs is unset).
  txsNote: string;
}

const segmentsOfOrigins = (origins: SelectionOrigins | undefined): Segment[] =>
  ORIGINS.map((origin) => ({
    origin,
    txs: origins?.[origin].txs ?? 0,
    gas: origins?.[origin].gas ?? 0,
    blobs: origins?.[origin].blobs ?? 0,
  }));

// segmentsOfTxs splits a block's transactions by origin (gas used where
// known, else the gas limit).
const segmentsOfTxs = (txs: NextBlockTx[]): Segment[] =>
  ORIGINS.map((origin) => {
    const own = txs.filter((tx) => tx.origin === origin);
    return {
      origin,
      txs: own.length,
      gas: own.reduce((sum, tx) => sum + (tx.gas_used ?? tx.gas), 0),
      blobs: own.reduce((sum, tx) => sum + (tx.blobs ?? 0), 0),
    };
  });

// segmentsUnsplit is a block known only by its totals: all public until its
// transactions say otherwise.
const segmentsUnsplit = (txs: number, gas: number, blobs: number): Segment[] =>
  ORIGINS.map((origin) => (origin === 'public' ? { origin, txs, gas, blobs } : { origin, txs: 0, gas: 0, blobs: 0 }));

const pct = (part: number, whole: number): number => (whole > 0 ? Math.min(100, (part / whole) * 100) : 0);

const skippedOf = (selection: TxSelectionSummary | undefined): [string, number][] => {
  const merged: Record<string, number> = {};
  for (const source of [selection?.skipped, selection?.fill_skipped, selection?.inclusion_list_skipped]) {
    for (const [reason, n] of Object.entries(source ?? {})) merged[reason] = (merged[reason] ?? 0) + n;
  }
  return Object.entries(merged)
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1]);
};

// ---------------------------------------------------------------------------
// The block view
// ---------------------------------------------------------------------------

const BlockMap: React.FC<{ txs: NextBlockTx[]; gasLimit: number }> = ({ txs, gasLimit }) => {
  const width = 600;
  const height = 90;
  const maxTip = txs.reduce((max, tx) => Math.max(max, Number(tx.tip)), 0) || 1;
  let x = 0;

  return (
    <svg className="bd-nb-map" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" role="img" aria-label="Block map">
      {txs.map((tx) => {
        const gas = tx.gas_used ?? tx.gas;
        const w = Math.max(0.6, (gas / (gasLimit || 1)) * width);
        const h = Math.max(3, Math.sqrt(Number(tx.tip) / maxTip) * (height - 4));
        const rect = (
          <rect key={tx.hash} x={x} y={height - h} width={w} height={h} className={`bd-nb-${tx.origin}`}>
            <title>
              {`${ORIGIN_LABELS[tx.origin]} ${tx.hash}\nfrom ${tx.from} nonce ${tx.nonce}\n${gas.toLocaleString()} gas · tip ${weiToGwei(tx.tip)} gwei${tx.blobs ? ` · ${tx.blobs} blobs` : ''}`}
            </title>
          </rect>
        );
        x += w;
        return rect;
      })}
    </svg>
  );
};

const TxList: React.FC<{ txs: NextBlockTx[]; total: number }> = ({ txs, total }) => {
  const [all, setAll] = useState(false);
  const shown = all ? txs : txs.slice(0, TX_LIST_ROWS);
  const used = txs.some((tx) => tx.gas_used !== undefined);

  return (
    <>
      <div className="table-responsive">
        <table className="table table-sm small mb-1">
          <thead>
            <tr>
              <th>#</th>
              <th>Origin</th>
              <th>Hash</th>
              <th>From</th>
              <th className="text-end">Nonce</th>
              <th className="text-end">{used ? 'Gas used' : 'Gas'}</th>
              <th className="text-end">Tip (gwei)</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((tx, i) => (
              <tr key={tx.hash}>
                <td className="bd-muted">{i + 1}</td>
                <td>
                  <span className={`bd-nb-swatch bd-nb-${tx.origin}`} /> {ORIGIN_LABELS[tx.origin]}
                  {tx.blobs ? <span className="bd-muted"> · {tx.blobs}b</span> : null}
                </td>
                <td className="font-monospace">
                  <CopyableHash value={tx.hash} chars={6} headOnly />
                </td>
                <td className="font-monospace">
                  <CopyableHash value={tx.from} chars={6} headOnly />
                </td>
                <td className="text-end">{tx.nonce}</td>
                <td className="text-end">{(tx.gas_used ?? tx.gas).toLocaleString()}</td>
                <td className="text-end">{weiToGwei(tx.tip)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {txs.length > TX_LIST_ROWS && (
        <button type="button" className="bd-btn bd-btn-sm" onClick={() => setAll(!all)}>
          {all ? 'Show fewer' : `Show all ${txs.length}${total > txs.length ? ` of ${total}` : ''}`}
        </button>
      )}
    </>
  );
};

// BlockView draws a block model. Every row is always there; a row without
// data shows a placeholder, so the card keeps its layout.
const BlockView: React.FC<{ title: string; slot: number; model: BlockModel }> = ({ title, slot, model }) => {
  const filled = model.segments.reduce((sum, s) => sum + s.gas, 0);
  const blobs: Origin[] = model.segments.flatMap((s) => Array<Origin>(s.blobs).fill(s.origin));
  const skipped = skippedOf(model.selection);
  const tag = VIEW_TAGS[model.kind];

  return (
    <div className="card bd-nb-card">
      <div className="card-body">
        <div className="bd-nb-head">
          <h3 className="bd-card-title mb-0">{title}</h3>
          <span className="bd-muted">slot {slot.toLocaleString()}</span>
          <Tag tone={tag.tone} title={tag.title}>
            {tag.label}
          </Tag>
          {model.tags}
        </div>
        <div className="bd-nb-desc">{model.description}</div>

        <div className="bd-nb-gasbar" role="img" aria-label="Gas by origin">
          {model.segments
            .filter((s) => s.gas > 0)
            .map((s) => (
              <div
                key={s.origin}
                className={`bd-nb-seg bd-nb-${s.origin}`}
                style={{ width: `${pct(s.gas, model.gasLimit)}%` }}
                title={`${ORIGIN_LABELS[s.origin]}: ${s.txs} txs, ${s.gas.toLocaleString()} gas`}
              />
            ))}
        </div>
        <div className="bd-nb-legend">
          {model.segments.map((s) => (
            <span key={s.origin} className="bd-nb-legend-item">
              <span className={`bd-nb-swatch bd-nb-${s.origin}`} />
              {ORIGIN_LABELS[s.origin]} <strong>{s.txs}</strong>
              <span className="bd-muted"> · {pct(s.gas, model.gasLimit).toFixed(1)}%</span>
            </span>
          ))}
          <span className="bd-nb-legend-item bd-muted ms-auto">
            {filled.toLocaleString()} / {model.gasLimit.toLocaleString()} gas {model.gasLabel}
          </span>
        </div>
        <div className="bd-nb-blobs" title={`${blobs.length} of ${model.maxBlobs} blobs`}>
          <span className="bd-muted small me-1">blobs</span>
          {Array.from({ length: Math.max(model.maxBlobs, blobs.length) }, (_, i) => (
            <span key={i} className={`bd-nb-blob${i < blobs.length ? ` bd-nb-${blobs[i]}` : ''}`} />
          ))}
          <span className="bd-muted small ms-1">
            {blobs.length}/{model.maxBlobs}
          </span>
        </div>

        <div className="bd-nb-stats">
          <StatTile
            label="Value"
            value={model.value ? `${weiToEth(model.value)} ETH` : '—'}
            note={model.valueNote}
            title="What the block pays its fee recipient in priority fees"
          />
          <StatTile
            label="Transactions"
            value={model.txCount !== undefined ? model.txCount.toLocaleString() : '—'}
            note={`${model.blobs} blobs`}
          />
          <StatTile
            label={`Gas ${model.gasLabel}`}
            value={`${pct(model.gas, model.gasLimit).toFixed(1)}%`}
            note={model.gas.toLocaleString()}
          />
          <StatTile label="Base fee" value={model.baseFee ? `${weiToGwei(model.baseFee)} gwei` : '—'} />
        </div>

        <div className="bd-nb-skips">
          <span className="bd-muted small">left out</span>
          {skipped.length === 0 ? (
            <span className="bd-muted small">—</span>
          ) : (
            skipped.slice(0, 6).map(([reason, n]) => (
              <Tag key={reason} title={reason}>
                {reason}: {n}
              </Tag>
            ))
          )}
        </div>

        <div className={model.txsStale ? 'bd-nb-stale' : undefined}>
          <div className="bd-nb-subtitle">Block map</div>
          {model.txs ? <BlockMap txs={model.txs} gasLimit={model.gasLimit} /> : <div className="bd-nb-map bd-nb-map-empty">{model.txsNote}</div>}
          <div className="bd-muted small mb-2">Width = gas, height = effective tip (square root scale).</div>
          {model.txs ? (
            <TxList txs={model.txs} total={model.txsTotal ?? model.txs.length} />
          ) : (
            <div className="bd-muted small">{model.txsNote}</div>
          )}
        </div>
      </div>
    </div>
  );
};

const VIEW_TAGS: Record<BlockModel['kind'], { label: string; tone: 'ok' | 'accent' | 'info'; title: string }> = {
  included: { label: 'included', tone: 'ok', title: "The slot's block is on chain" },
  candidate: { label: 'candidate', tone: 'accent', title: 'Our payload picked for the slot (not on chain yet)' },
  estimate: { label: 'estimate', tone: 'info', title: 'What our selection would put into the block now' },
};

// ---------------------------------------------------------------------------
// Sources: a built block (ours or anyone's) and an estimate
// ---------------------------------------------------------------------------

const PAYLOAD_TXS_RETRIES = 8;

// usePayloadTransactions loads a block's transactions: ours from the payload
// cache, any other from the chain (a testing build's before it is on chain
// needs a login: locked is set without one). A block's beacon event can come
// before the EL imported it, so a 404 is retried a few times. While a new
// hash of the same slot loads, the previous list stays (stale).
function usePayloadTransactions(slot: number, hash: string | undefined) {
  const [loaded, setLoaded] = useState<{ slot: number; data: PayloadTransactionsResponse } | null>(null);
  const [locked, setLocked] = useState(false);
  const { getAuthHeader, isLoggedIn } = useAuth();

  useEffect(() => {
    setLocked(false);
    if (!hash) return undefined;

    let cancelled = false;
    let retry: ReturnType<typeof setTimeout> | undefined;

    const load = async (attempt: number) => {
      try {
        const headers: HeadersInit = {};
        const token = await getAuthHeader();
        if (token) headers['Authorization'] = `Bearer ${token}`;
        const response = await fetch(apiPath(`/payloads/${hash}/transactions`), { headers });
        if (cancelled) return;
        if (response.status === 401) {
          setLocked(true);
          return;
        }
        if (response.status === 404 && attempt < PAYLOAD_TXS_RETRIES) {
          retry = setTimeout(() => load(attempt + 1), 1000);
          return;
        }
        if (response.ok) setLoaded({ slot, data: await response.json() });
      } catch {
        // the list is an addition: the card works without it
      }
    };

    load(0);

    return () => {
      cancelled = true;
      if (retry) clearTimeout(retry);
    };
  }, [slot, hash, getAuthHeader, isLoggedIn]);

  const data = loaded?.slot === slot ? loaded.data : null;
  const stale = !!data && !!hash && data.block_hash.toLowerCase() !== hash.toLowerCase();

  return { data, stale, locked };
}

// BuiltBlockCard is a built block — our candidate, our included payload or
// another builder's block — as a block model.
const BuiltBlockCard: React.FC<{
  title: string;
  slot: number;
  state: SlotState;
  kind: 'included' | 'candidate';
  hash: string;
  ours?: PayloadInfo;
  maxBlobs: number;
}> = ({ title, slot, state, kind, hash, ours, maxBlobs }) => {
  const transactions = usePayloadTransactions(slot, hash);
  const loaded = transactions.data;
  const txs = loaded?.txs;

  const run =
    ours?.build.kind === 'testing' && ours.build.run !== undefined
      ? Object.values(state.targets ?? {})
          .map((t) => t.testingRuns[ours.build.run!])
          .find((r) => r?.payloadHash?.toLowerCase() === ours.block_hash.toLowerCase())
      : undefined;
  const selection = run?.info?.selection;
  const target = primaryTarget(state);
  const engine = ours && target?.engineBuild?.payloadHash ? state.payloads?.[target.engineBuild.payloadHash] : undefined;

  const detail = state.blockDetail;
  const envelope = state.payloadAvailableDetail;
  const gasLimit = loaded?.gas_limit ?? ours?.gas_limit ?? detail?.payload?.gas_limit ?? envelope?.gas_limit ?? detail?.bid?.gas_limit ?? 0;
  const gasUsed = loaded?.gas_used ?? ours?.gas_used ?? detail?.payload?.gas_used ?? envelope?.gas_used ?? 0;
  const txCount = txs?.length ?? ours?.num_transactions ?? detail?.payload?.num_transactions ?? envelope?.num_transactions;
  const blobCount = ours?.num_blobs ?? detail?.num_blob_commitments ?? detail?.bid?.num_blob_kzgs ?? 0;

  const model: BlockModel = {
    kind,
    tags: ours ? <Tag tone="accent">ours · {buildLabel(ours.build)}</Tag> : <Tag>other builder</Tag>,
    description: (
      <>
        {ours
          ? ours.build.kind === 'engine'
            ? 'Our engine build: the EL picked from its own, the public, mempool'
            : `Our testing build from ${TX_SOURCE_LABELS[run?.info?.tx_source ?? ''] ?? run?.info?.tx_source ?? 'the pool'}`
          : `Built by ${detail?.bid ? `builder ${detail.bid.builder_index}` : 'another builder'}`}{' '}
        · <span className="font-monospace"><CopyableHash value={hash} chars={6} headOnly /></span>
      </>
    ),
    gasLimit,
    gas: gasUsed,
    gasLabel: 'used',
    segments: txs
      ? segmentsOfTxs(txs)
      : selection?.origins
        ? segmentsOfOrigins(selection.origins)
        : segmentsUnsplit(txCount ?? 0, gasUsed, blobCount),
    maxBlobs,
    value: loaded?.value ?? ours?.block_value ?? (detail?.bid ? String(BigInt(detail.bid.value_gwei) * 1_000_000_000n) : undefined),
    valueNote: engine && ours && engine !== ours ? `engine ${weiToEth(engine.block_value)}` : undefined,
    txCount,
    blobs: blobCount,
    baseFee: loaded?.base_fee ?? ours?.base_fee_per_gas ?? envelope?.base_fee_per_gas,
    selection,
    txs,
    txsStale: transactions.stale,
    txsNote: transactions.locked
      ? 'A custom-built payload may hold private transactions: log in to see them.'
      : 'Loading the transactions…',
  };

  return <BlockView title={title} slot={slot} model={model} />;
};

const estimateModel = (estimate: NextBlockResponse | undefined, maxBlobs: number, isLoggedIn: boolean): BlockModel => {
  const selection = estimate?.selection;
  const segments = segmentsOfOrigins(selection?.origins);
  const tips = selection?.origins ? ORIGINS.reduce((sum, o) => sum + BigInt(selection.origins![o].tips || '0'), 0n) : undefined;

  const basis =
    estimate?.basis === 'candidate' && estimate.basis_block ? (
      <>
        after our current slot candidate{' '}
        <span className="font-monospace"><CopyableHash value={estimate.basis_block} chars={6} headOnly /></span>, assumed
        included
      </>
    ) : estimate?.basis === 'head_only' ? (
      'after a current slot block we did not build (nothing of it assumed)'
    ) : (
      'on the current head'
    );

  return {
    kind: 'estimate',
    tags: estimate ? (
      <>
        <Tag tone="accent">{TX_SOURCE_LABELS[estimate.tx_source] ?? estimate.tx_source}</Tag>
        {(estimate.tx_source === 'txpool' || estimate.tx_source === 'private_first') && (
          <Tag title="Order of the private mempool">{estimate.ordering}</Tag>
        )}
      </>
    ) : null,
    description: !estimate ? (
      'Estimating…'
    ) : estimate.note ? (
      estimate.note
    ) : (
      <>
        Our selection {basis}
        {!estimate.local_build_enabled && ' · the testing build is off'}
        {estimate.public_mempool === 'idle' && ' · the public mempool is not mirrored right now'}
      </>
    ),
    gasLimit: estimate?.gas_limit ?? 0,
    gas: segments.reduce((sum, s) => sum + s.gas, 0),
    gasLabel: 'reserved',
    segments,
    maxBlobs: estimate?.max_blobs ?? maxBlobs,
    value: tips?.toString(),
    valueNote: tips !== undefined ? 'at most (all gas used)' : undefined,
    txCount: selection ? segments.reduce((sum, s) => sum + s.txs, 0) : undefined,
    blobs: segments.reduce((sum, s) => sum + s.blobs, 0),
    baseFee: selection?.base_fee,
    selection,
    txs: estimate?.txs,
    txsTotal: estimate?.txs_total,
    txsNote: !estimate || !selection ? 'Estimating…' : isLoggedIn ? 'Loading the transactions…' : 'Log in to see the transactions.',
  };
};

// SlotCard is one slot of the clock with the best of what exists for it:
// the block on chain, our picked candidate, or the estimate of our
// selection.
const SlotCard: React.FC<{
  title: string;
  slot: number;
  state: SlotState | undefined;
  estimates: NextBlockEstimates | null;
  isLoggedIn: boolean;
}> = ({ title, slot, state, estimates, isLoggedIn }) => {
  const maxBlobs = estimates?.estimates[0]?.max_blobs ?? 0;

  const included = state?.blockDetail?.payload?.block_hash ?? state?.blockDetail?.bid?.block_hash;
  if (state && included) {
    const ours = state.payloads?.[included] ?? state.payloads?.[included.toLowerCase()];
    return <BuiltBlockCard title={title} slot={slot} state={state} kind="included" hash={included} ours={ours} maxBlobs={maxBlobs} />;
  }

  const pick = lastSelection(primaryTarget(state));
  const candidate = pick ? state?.payloads?.[pick.payloadHash] : undefined;
  if (state && candidate) {
    return (
      <BuiltBlockCard title={title} slot={slot} state={state} kind="candidate" hash={candidate.block_hash} ours={candidate} maxBlobs={maxBlobs} />
    );
  }

  const estimate = estimates?.estimates.find((e) => e.slot === slot);
  return <BlockView title={title} slot={slot} model={estimateModel(estimate, maxBlobs, isLoggedIn)} />;
};

interface NextBlockTabProps {
  currentSlot: number;
  slotStates: Record<number, SlotState>;
  estimates: NextBlockEstimates | null;
  isLoggedIn: boolean;
}

// NextBlockTab shows the current and the next slot of the clock side by
// side, each with the best of what exists for it.
export const NextBlockTab: React.FC<NextBlockTabProps> = ({ currentSlot, slotStates, estimates, isLoggedIn }) => (
  <div className="bd-nb-grid">
    {[
      { title: 'Current slot', slot: currentSlot },
      { title: 'Next slot', slot: currentSlot + 1 },
    ].map(({ title, slot }) => (
      <SlotCard key={title} title={title} slot={slot} state={slotStates[slot]} estimates={estimates} isLoggedIn={isLoggedIn} />
    ))}
  </div>
);
