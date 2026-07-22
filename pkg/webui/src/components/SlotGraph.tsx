import React, { useState, useCallback, useRef } from 'react';
import type { SlotState, Config, ChainInfo, OurBid, ExternalBid, HeadVoteDataPoint, ServiceStatus } from '../types';
import { formatGwei, isSlotScheduled, calculateSlotTiming, calculatePosition } from '../utils';
import { Popover, PopoverData } from './Popover';
import { HeadVoteHeatmap } from './HeadVoteHeatmap';
import { CurrentTimeIndicator } from './CurrentTimeIndicator';
import { BuildDelayLine } from './BuildDelayLine';

const ROW_HEIGHT = 22;
const ROW_PAD = 3;

interface SlotGraphProps {
  slot: number;
  state: SlotState;
  originalConfig: Config | null;
  originalServiceStatus: ServiceStatus | null;
  currentConfig: Config | null;
  chainInfo: ChainInfo | null;
  currentDisplaySlot: number;
  serviceStatus: ServiceStatus | null;
  hideHeadVotes?: boolean;
}

interface PopoverState {
  data: PopoverData;
  x: number;
  y: number;
  // Set for the head-votes popover: embeds the per-name arrival heatmap.
  headVoteSlot?: number;
}

// Maps participation percentage (0-100) to SVG Y coordinate (100=bottom, 0=top).
const pctToY = (pct: number): number => {
  return 100 - Math.max(0, Math.min(100, pct));
};

// Glow band heights below the line (viewBox units): stacked translucent
// bands approximate a soft gradient hanging under the curve — wide enough
// to read as a filled area, fading out well before the bottom. Painted
// outer to inner; opacities accumulate towards the line.
const HEAD_VOTES_BANDS: { height: number; opacity: number }[] = [
  { height: 30, opacity: 0.02 },
  { height: 20, opacity: 0.035 },
  { height: 12, opacity: 0.055 },
  { height: 6, opacity: 0.08 },
];

// After the last data point the line (and its glow) extend this far and
// fade out, so the curve visibly "runs off" instead of stopping abruptly.
const HEAD_VOTES_EXTEND = 8;

// Corner radius of the rounded steps, per axis. The viewBox is stretched
// (100 units span ~600-1000px horizontally but only ~50-70px vertically),
// so a visually circular ~3px corner needs very different unit radii.
const HEAD_VOTES_CORNER_RX = 0.5;
const HEAD_VOTES_CORNER_RY = 5;

interface StepPoint {
  x: number;
  y: number;
}

// roundedStepPath renders an orthogonal step polyline with rounded corners
// (quadratic curves shortcutting each 90° turn) so the curve reads as a
// designed chart rather than blocky steps. Returns the path without the
// leading M command so callers can compose closed regions from it.
const roundedStepPath = (pts: StepPoint[]): string => {
  const segments: string[] = [];

  for (let i = 1; i < pts.length; i++) {
    const prev = pts[i - 1];
    const cur = pts[i];
    const next = pts[i + 1];

    if (!next) {
      segments.push(`L ${cur.x} ${cur.y}`);
      break;
    }

    // Per-axis corner radii, clamped to half the adjoining segment lengths.
    const horizontalIn = cur.y === prev.y;
    const inLen = horizontalIn ? Math.abs(cur.x - prev.x) : Math.abs(cur.y - prev.y);
    const outLen = horizontalIn ? Math.abs(next.y - cur.y) : Math.abs(next.x - cur.x);
    const rIn = Math.min(horizontalIn ? HEAD_VOTES_CORNER_RX : HEAD_VOTES_CORNER_RY, inLen / 2);
    const rOut = Math.min(horizontalIn ? HEAD_VOTES_CORNER_RY : HEAD_VOTES_CORNER_RX, outLen / 2);

    const dirInX = Math.sign(cur.x - prev.x);
    const dirInY = Math.sign(cur.y - prev.y);
    const dirOutX = Math.sign(next.x - cur.x);
    const dirOutY = Math.sign(next.y - cur.y);

    const approach = { x: cur.x - dirInX * rIn, y: cur.y - dirInY * rIn };
    const depart = { x: cur.x + dirOutX * rOut, y: cur.y + dirOutY * rOut };

    segments.push(`L ${approach.x} ${approach.y}`);
    segments.push(`Q ${cur.x} ${cur.y} ${depart.x} ${depart.y}`);
  }

  return segments.join(' ');
};

// Builds the participation step path plus the glow bands below it.
// Participation is a step function — it only rises when a vote arrives — so
// segments run horizontally at the reached level and jump vertically at each
// data point (with rounded corners). The graph is deliberately subtle
// background information: a thin translucent line with a faint under-glow,
// no area fill.
const buildHeadVotesPaths = (
  points: HeadVoteDataPoint[],
  slotStartTime: number,
  rangeStart: number,
  totalRange: number
): {
  linePath: string;
  bands: { path: string; opacity: number }[]; // outer first (painted in order)
  startX: number;
  endX: number;
  fadeFromX: number; // last data point; the fade-out runs from here to endX
} | null => {
  if (points.length === 0) return null;

  const coords = points
    .map(p => ({
      x: ((p.time - slotStartTime - rangeStart) / totalRange) * 100,
      y: pctToY(p.pct)
    }))
    .sort((a, b) => a.x - b.x);

  // Step waypoints: hold the previous level to each point's x, then jump.
  const pts: StepPoint[] = [{ x: coords[0].x, y: coords[0].y }];
  let prevY = coords[0].y;

  for (let i = 1; i < coords.length; i++) {
    const c = coords[i];
    pts.push({ x: c.x, y: prevY });
    if (c.y !== prevY) {
      pts.push({ x: c.x, y: c.y });
      prevY = c.y;
    }
  }

  // Fading run-off past the last data point (clamped to the cell edge).
  const fadeFromX = pts[pts.length - 1].x;
  const extendX = Math.min(fadeFromX + HEAD_VOTES_EXTEND, 100);

  if (extendX > fadeFromX) {
    pts.push({ x: extendX, y: prevY });
  }

  const start = `M ${pts[0].x} ${pts[0].y}`;
  const linePath = pts.length > 1 ? `${start} ${roundedStepPath(pts)}` : start;

  // A band is the closed region between the (rounded) line and the line
  // shifted down by h (clamped to the bottom; the hidden underside stays
  // unrounded — invisible at these opacities).
  const band = (h: number): string =>
    linePath +
    ' L ' +
    [...pts].reverse().map(p => `${p.x} ${Math.min(100, p.y + h)}`).join(' L ') +
    ' Z';

  return {
    linePath,
    bands: HEAD_VOTES_BANDS.map(b => ({ path: band(b.height), opacity: b.opacity })),
    startX: pts[0].x,
    endX: pts[pts.length - 1].x,
    fadeFromX
  };
};

export const SlotGraph: React.FC<SlotGraphProps> = ({
  slot,
  state,
  originalConfig,
  originalServiceStatus,
  currentConfig,
  chainInfo,
  currentDisplaySlot,
  serviceStatus,
  hideHeadVotes
}) => {
  const [popover, setPopover] = useState<PopoverState | null>(null);

  // Capture config and service status on FIRST RENDER - never change after that
  const capturedConfigRef = useRef<Config | null>(null);
  if (capturedConfigRef.current === null) {
    capturedConfigRef.current = originalConfig || currentConfig;
  }

  const capturedServiceStatusRef = useRef<ServiceStatus | null>(null);
  if (capturedServiceStatusRef.current === null) {
    capturedServiceStatusRef.current = originalServiceStatus || serviceStatus;
  }

  const { slotStartTime, rangeStart, totalRange } = calculateSlotTiming(chainInfo, slot);
  const slotDuration = chainInfo?.seconds_per_slot || 12000;
  const genesisTime = chainInfo?.genesis_time || 0;

  // Only use current (live) values for FUTURE slots, otherwise use captured snapshots
  const isSlotInFuture = slot > currentDisplaySlot;
  const config = isSlotInFuture ? currentConfig : capturedConfigRef.current;
  const effectiveServiceStatus = isSlotInFuture ? serviceStatus : capturedServiceStatusRef.current;

  const epbsConfig = config?.epbs;
  const isScheduled = state.scheduled !== undefined ? state.scheduled : isSlotScheduled(slot, config?.schedule);

  // Dynamic row layout based on active services (frozen for past slots)
  const epbsActive = effectiveServiceStatus?.epbs_enabled ?? true;
  const builderApiActive = effectiveServiceStatus?.builder_api_enabled ?? false;

  // Rows from top to bottom:
  //   chain+payload (always) | ePBS (if active) | Builder API (if active)
  // When both active: 3 rows. When one active: 2 rows. When none: 2 rows (chain + empty).
  const activeRows = 2 + (epbsActive && builderApiActive ? 1 : 0);
  const graphHeight = activeRows * ROW_HEIGHT + 2 * ROW_PAD;

  // Row positions from bottom: Builder API (if active) -> ePBS (if active) -> chain (top)
  // When neither builder is active, an empty row sits below chain.
  let rowIdx = 0;
  const builderApiRowBottom = builderApiActive ? (rowIdx++) * ROW_HEIGHT + ROW_PAD : -1;
  const epbsRowBottom = epbsActive ? (rowIdx++) * ROW_HEIGHT + ROW_PAD : -1;
  if (!epbsActive && !builderApiActive) rowIdx++; // empty second row
  const chainRowBottom = (rowIdx++) * ROW_HEIGHT + ROW_PAD;

  // Slot label
  let slotLabel = `Slot ${slot}`;
  if (slot === currentDisplaySlot) {
    slotLabel += ' (now)';
  } else if (slot === currentDisplaySlot + 1) {
    slotLabel += ' (next)';
  }

  const showPopover = useCallback((e: React.MouseEvent, data: PopoverData) => {
    e.stopPropagation();
    const rect = (e.target as HTMLElement).getBoundingClientRect();
    let x = rect.left;
    if (x + 320 > window.innerWidth) {
      x = window.innerWidth - 330;
    }
    setPopover({ data, x, y: rect.bottom + 5 });
  }, []);

  const closePopover = useCallback(() => {
    setPopover(null);
  }, []);

  // Calculate marker positions
  const slotStartX = calculatePosition(0, rangeStart, totalRange);
  const pct25X = calculatePosition(slotDuration * 0.25, rangeStart, totalRange);
  const pct50X = calculatePosition(slotDuration * 0.50, rangeStart, totalRange);
  const pct75X = calculatePosition(slotDuration * 0.75, rangeStart, totalRange);
  const slotEndX = calculatePosition(slotDuration, rangeStart, totalRange);

  // Bid window and reveal marker
  const bidStartX = epbsConfig ? calculatePosition(epbsConfig.bid_start_time, rangeStart, totalRange) : 0;
  const bidEndX = epbsConfig ? calculatePosition(epbsConfig.bid_end_time, rangeStart, totalRange) : 0;
  // The reveal marker shows the time gate; a pure vote-gated reveal has no
  // fixed time to mark.
  const revealConfig = config?.reveal;
  const revealTimeGated = (revealConfig?.gate_mode ?? 'time') !== 'vote';
  const revealX = revealConfig && revealTimeGated
    ? calculatePosition(revealConfig.time_ms, rangeStart, totalRange)
    : -1;

  // Build delay line: from build start to payloadCreatedAt (or live "now" while building).
  // Prefer the actual build-started time (accurate even when building immediately);
  // fall back to the configured build_start_time for slots without a start event.
  const buildStartMs = state.payloadBuildStartedAt !== undefined
    ? state.payloadBuildStartedAt - slotStartTime
    : (epbsConfig ? epbsConfig.build_start_time : 0);
  const buildStartX = calculatePosition(buildStartMs, rangeStart, totalRange);
  const buildFailed = state.payloadBuildFailed === true;
  // The build span finalizes at the payload-ready time (success) or the failure time.
  const buildEndAt = state.payloadCreatedAt ?? state.payloadBuildFailedAt;
  const buildActive = state.payloadBuildStartedAt !== undefined
    || state.payloadCreatedAt !== undefined
    || buildFailed;

  // While a build is in progress, the line grows toward "now" but is capped at the
  // expected completion (build start + payload_build_time) so a stuck/timed-out build
  // shows a bounded bar instead of creeping to the slot end. payload_build_time lives
  // at the root of the SSE config object.
  const payloadBuildTime = (config as unknown as Record<string, unknown>)?.payload_build_time;
  const expectedBuildEndAt = typeof payloadBuildTime === 'number'
    ? slotStartTime + buildStartMs + payloadBuildTime
    : undefined;

  const truncateHash = (hash: string, len = 16) => {
    if (hash.length <= len + 3) return hash;
    return hash.substring(0, len) + '...';
  };

  const renderEventDot = (
    className: string,
    timeMs: number,
    data: PopoverData,
    key: string
  ) => {
    const x = calculatePosition(timeMs, rangeStart, totalRange);
    if (x > 100) return null;

    // Clamp to left border if event occurred before the displayed range
    const clampedX = Math.max(0, x);

    return (
      <div
        key={key}
        className={`event-dot ${className}`}
        style={{ left: `${clampedX}%` }}
        onClick={(e) => showPopover(e, data)}
      />
    );
  };

  return (
    <div className="slot-graph" style={{ height: `${graphHeight}px` }} onClick={closePopover}>
      <div className="slot-graph-label">
        <span className="slot-graph-slot-label" style={{ bottom: `${chainRowBottom}px`, height: `${ROW_HEIGHT}px` }}>{slotLabel}</span>
        {epbsActive && epbsRowBottom >= 0 && (
          <span className="slot-graph-row-label" style={{ bottom: `${epbsRowBottom}px`, height: `${ROW_HEIGHT}px` }}>ePBS</span>
        )}
        {builderApiActive && builderApiRowBottom >= 0 && (
          <span className="slot-graph-row-label" style={{ bottom: `${builderApiRowBottom}px`, height: `${ROW_HEIGHT}px` }}>Builder API</span>
        )}
      </div>
      <div className="slot-graph-area">
        <div className="slot-graph-bg">
          {/* Slot markers */}
          <div className="slot-marker slot-start-marker" style={{ left: `${slotStartX}%` }}>
            <span className="marker-label">0%</span>
          </div>
          <div className="slot-marker pct-marker" style={{ left: `${pct25X}%` }}>
            <span className="marker-label">25%</span>
          </div>
          <div className="slot-marker pct-marker" style={{ left: `${pct50X}%` }}>
            <span className="marker-label">50%</span>
          </div>
          <div className="slot-marker pct-marker" style={{ left: `${pct75X}%` }}>
            <span className="marker-label">75%</span>
          </div>
          <div className="slot-marker slot-end-marker" style={{ left: `${slotEndX}%` }}>
            <span className="marker-label">100%</span>
          </div>

          {/* Bid window - spans ePBS row only */}
          {epbsActive && epbsRowBottom >= 0 && epbsConfig && bidStartX < 100 && bidEndX > 0 && (
            <div
              className={`bid-window-bg ${!isScheduled ? 'bid-window-disabled' : ''}`}
              style={{
                bottom: `${epbsRowBottom}px`,
                height: `${ROW_HEIGHT}px`,
                left: `${Math.max(0, bidStartX)}%`,
                width: `${Math.min(100, bidEndX) - Math.max(0, bidStartX)}%`
              }}
            >
              <span className="duty-label bid-label">
                Bid: {epbsConfig.bid_start_time} - {epbsConfig.bid_end_time}ms
              </span>
            </div>
          )}

          {/* Reveal marker (time gate) - spans ePBS row only */}
          {epbsActive && epbsRowBottom >= 0 && revealConfig && revealX >= 0 && revealX <= 100 && (
            <div
              className={`reveal-marker ${!state.bidWon ? 'reveal-marker-disabled' : ''}`}
              style={{
                bottom: `${epbsRowBottom}px`,
                height: `${ROW_HEIGHT}px`,
                left: `${revealX}%`
              }}
            >
              <span className="duty-label reveal-label">
                Reveal: {revealConfig.time_ms}ms
                {revealConfig.gate_mode === 'vote_or_time' ? ' (or vote)' : ''}
                {revealConfig.gate_mode === 'vote_and_time' ? ' (and vote)' : ''}
              </span>
            </div>
          )}
        </div>

        {/* Head votes participation graph (hidden when subnet coverage is too
            low to trust the raw single-attestation view) */}
        {!hideHeadVotes && state.headVotes && state.headVotes.length > 0 && (() => {
          const paths = buildHeadVotesPaths(state.headVotes, slotStartTime, rangeStart, totalRange);
          if (!paths) return null;

          const { linePath, bands, startX, endX, fadeFromX } = paths;
          const totalWidth = endX - startX;
          const fadeOffset = totalWidth > 0
            ? `${Math.max(0, Math.min(100, ((fadeFromX - startX) / totalWidth) * 100))}%`
            : '100%';
          const maxVote = state.headVotes.reduce((best, p) => p.pct > best.pct ? p : best, state.headVotes[0]);

          // Threshold-met marker: the curve turns green from the crossing
          // point onward; the marker sits at the threshold height on the
          // crossing jump.
          const thresholdPct = state.headVoteThresholdPct ?? 0;
          const metAt = state.headVoteThresholdMetAt;
          const metX = metAt !== undefined
            ? ((metAt - slotStartTime - rangeStart) / totalRange) * 100
            : null;
          const metOffset = metX !== null && totalWidth > 0
            ? `${Math.max(0, Math.min(100, ((metX - startX) / totalWidth) * 100))}%`
            : null;
          const lineStroke = metOffset !== null ? `url(#hvline-${slot})` : '#00bcd4';

          const popoverItems = [
            { label: 'Max Participation', value: `${maxVote.pct.toFixed(1)}%` },
            { label: 'Participating ETH', value: `${maxVote.eth.toLocaleString()} ETH` }
          ];
          if (maxVote.voteCount !== undefined) {
            popoverItems.push({ label: 'Votes', value: maxVote.voteCount.toLocaleString() });
          }
          if (thresholdPct > 0) {
            popoverItems.push({ label: 'Threshold', value: `${thresholdPct.toFixed(0)}%` });
            popoverItems.push({
              label: 'Threshold Met',
              value: metAt !== undefined
                ? `${((metAt - slotStartTime) / 1000).toFixed(1)}s into slot`
                : 'no'
            });
          }

          return (
            <>
              <svg
                className="head-votes-graph"
                viewBox="0 0 100 100"
                preserveAspectRatio="none"
                style={{ overflow: 'visible' }}
              >
                <defs>
                  {metOffset !== null && (
                    <linearGradient
                      id={`hvline-${slot}`}
                      gradientUnits="userSpaceOnUse"
                      x1={String(startX)} y1="0" x2={String(endX)} y2="0"
                    >
                      <stop offset={metOffset} stopColor="#00bcd4" />
                      <stop offset={metOffset} stopColor="#28a745" />
                    </linearGradient>
                  )}
                  {/* Fade-out over the run-off past the last data point */}
                  <linearGradient
                    id={`hvfade-${slot}`}
                    gradientUnits="userSpaceOnUse"
                    x1={String(startX)} y1="0" x2={String(endX)} y2="0"
                  >
                    <stop offset="0%" stopColor="white" stopOpacity="1" />
                    <stop offset={fadeOffset} stopColor="white" stopOpacity="1" />
                    <stop offset="100%" stopColor="white" stopOpacity="0" />
                  </linearGradient>
                  <mask id={`hvmask-${slot}`}>
                    <rect x={startX} y="0" width={totalWidth} height="100" fill={`url(#hvfade-${slot})`} />
                  </mask>
                </defs>
                <g mask={`url(#hvmask-${slot})`}>
                  {/* Soft under-glow: stacked translucent bands below the line */}
                  {bands.map((b, i) => (
                    <path key={i} d={b.path} fill={`rgba(0, 188, 212, ${b.opacity})`} />
                  ))}
                  <path
                    d={linePath}
                    fill="none"
                    stroke={lineStroke}
                    strokeWidth="1"
                    strokeOpacity="0.6"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    vectorEffect="non-scaling-stroke"
                  />
                </g>
                {/* Wider invisible hit area for click interaction */}
                <path
                  d={linePath}
                  fill="none"
                  stroke="transparent"
                  strokeWidth="12"
                  vectorEffect="non-scaling-stroke"
                  style={{ pointerEvents: 'stroke', cursor: 'pointer' }}
                  onClick={(e) => {
                    e.stopPropagation();
                    setPopover({
                      data: {
                        title: 'Head Vote Participation',
                        items: popoverItems
                      },
                      x: Math.min(e.clientX, window.innerWidth - 470),
                      y: e.clientY + 10,
                      headVoteSlot: slot
                    });
                  }}
                />
              </svg>
              {metX !== null && metX >= 0 && metX <= 100 && (
                <div
                  className="head-votes-met-marker"
                  style={{
                    left: `${metX}%`,
                    bottom: `${Math.min(100, thresholdPct)}%`
                  }}
                  title={`Vote threshold (${thresholdPct.toFixed(0)}%) met`}
                />
              )}
            </>
          );
        })()}

        {/* Chain + payload events row (always on top) */}
        <div className="event-row chain-events" style={{ bottom: `${chainRowBottom}px` }}>
          {/* Build delay line: from build start to payload ready (animates live while building).
              On failure we render a red dot (below) instead — a thin line is too hard to see/click. */}
          {epbsConfig && buildActive && !buildFailed && genesisTime > 0 && buildStartX < 100 && (
            <BuildDelayLine
              leftPct={buildStartX}
              slotStartTime={slotStartTime}
              rangeStart={rangeStart}
              totalRange={totalRange}
              endAt={buildEndAt}
              expectedEndAt={expectedBuildEndAt}
              onClick={(e) => showPopover(e, {
                title: 'Build Delay',
                items: state.payloadCreatedAt
                  ? [
                      { label: 'Build Start', value: `${buildStartMs}ms` },
                      { label: 'Payload Ready', value: `${state.payloadCreatedAt - slotStartTime}ms` },
                      { label: 'Duration', value: `${(state.payloadCreatedAt - slotStartTime) - buildStartMs}ms` }
                    ]
                  : [
                      { label: 'Build Start', value: `${buildStartMs}ms` },
                      { label: 'Status', value: 'Building…' }
                    ]
              })}
            />
          )}

          {/* Build failed — red dot at the failure time (falls back to build start) with error details */}
          {epbsConfig && buildFailed && genesisTime > 0 && renderEventDot(
            'build-failed',
            (state.payloadBuildFailedAt ?? (slotStartTime + buildStartMs)) - slotStartTime,
            {
              title: 'Build Failed',
              items: [
                { label: 'Build Start', value: `${buildStartMs}ms` },
                ...(state.payloadBuildFailedAt ? [{ label: 'Failed At', value: `${state.payloadBuildFailedAt - slotStartTime}ms` }] : []),
                { label: 'Error', value: state.payloadBuildError || 'unknown error' }
              ]
            },
            'build-failed'
          )}

          {/* Payload attributes for the next slot — each arrives during this slot
              (the CL re-emits one per head update), so render one dot per event
              here (on the parent slot's graph) with details on click. */}
          {genesisTime > 0 && state.nextSlotAttributes?.map((attr, i) => renderEventDot(
            'payload-attributes',
            attr.receivedAt - slotStartTime,
            {
              title: `Payload Attributes (slot ${attr.proposalSlot})`,
              items: [
                { label: 'For Slot', value: `${attr.proposalSlot}` },
                { label: 'Update', value: `${i + 1} of ${state.nextSlotAttributes!.length}` },
                { label: 'Proposer', value: `${attr.proposerIndex}` },
                { label: 'Received', value: `${attr.receivedAt - slotStartTime}ms` },
                {
                  label: 'Parent Hash',
                  value: truncateHash(attr.parentBlockHash),
                  copyValue: attr.parentBlockHash
                },
                {
                  label: 'Parent Root',
                  value: truncateHash(attr.parentBlockRoot),
                  copyValue: attr.parentBlockRoot
                },
                { label: 'Parent Block #', value: `${attr.parentBlockNumber}` },
                { label: 'Gas Limit', value: `${attr.targetGasLimit}` },
                { label: 'Withdrawals', value: `${attr.withdrawalsCount}` },
                {
                  label: 'Fee Recipient',
                  value: truncateHash(attr.feeRecipient),
                  copyValue: attr.feeRecipient
                }
              ]
            },
            `payload-attributes-${i}`
          ))}

          {/* Payload created */}
          {state.payloadCreatedAt && genesisTime > 0 && renderEventDot(
            'payload-created',
            state.payloadCreatedAt - slotStartTime,
            {
              title: 'Payload Created',
              items: [
                { label: 'Time', value: `${state.payloadCreatedAt - slotStartTime}ms` },
                ...(state.payloadBlockHash ? [{
                  label: 'Block Hash',
                  value: truncateHash(state.payloadBlockHash),
                  copyValue: state.payloadBlockHash
                }] : []),
                ...(state.payloadBlockValue ? [{
                  label: 'Block Value',
                  value: formatGwei(state.payloadBlockValue)
                }] : [])
              ]
            },
            'payload'
          )}

          {/* Block received — with crown when block contains our payload */}
          {state.blockReceivedAt && genesisTime > 0 && (() => {
            const blockMs = state.blockReceivedAt! - slotStartTime;
            const x = calculatePosition(blockMs, rangeStart, totalRange);
            if (x > 100) return null;
            const clampedX = Math.max(0, x);
            const won = !!state.bidWon;
            const popoverData: PopoverData = {
              title: won ? 'Block Won!' : 'Block Received',
              items: [
                { label: 'Time', value: `${blockMs}ms` },
                ...(state.blockRoot ? [{
                  label: 'Block Root',
                  value: truncateHash(state.blockRoot),
                  copyValue: state.blockRoot
                }] : []),
                ...(won ? [{ label: 'Result', value: 'Our payload was included in this block' }] : [])
              ]
            };
            return (
              <div
                key="block"
                className={`event-dot block-received`}
                style={{ left: `${clampedX}%` }}
                onClick={(e) => showPopover(e, popoverData)}
              >
                {won && <i className="fas fa-crown event-dot-crown" />}
              </div>
            );
          })()}

          {/* Payload available */}
          {state.payloadAvailableAt && genesisTime > 0 && renderEventDot(
            'payload-available',
            state.payloadAvailableAt - slotStartTime,
            {
              title: 'Payload Available',
              items: [
                { label: 'Time', value: `${state.payloadAvailableAt - slotStartTime}ms` },
                ...(state.payloadAvailableBlockHash ? [{
                  label: 'Payload Hash',
                  value: truncateHash(state.payloadAvailableBlockHash),
                  copyValue: state.payloadAvailableBlockHash
                }] : []),
                ...(state.payloadAvailableBuilder !== undefined ? [{
                  label: 'Builder Index',
                  value: String(state.payloadAvailableBuilder)
                }] : [])
              ]
            },
            'envelope'
          )}

          {/* External bids */}
          {state.externalBids?.map((bid: ExternalBid, idx: number) => {
            const bidMs = bid.time - slotStartTime;
            return renderEventDot(
              'external-bid',
              bidMs,
              {
                title: 'External Bid',
                items: [
                  { label: 'Time', value: `${bidMs}ms` },
                  { label: 'Bid Amount', value: formatGwei(bid.value) },
                  { label: 'Builder Index', value: String(bid.builder) },
                  ...(bid.blockHash ? [{
                    label: 'Block Hash',
                    value: truncateHash(bid.blockHash),
                    copyValue: bid.blockHash
                  }] : [])
                ]
              },
              `ext-bid-${idx}`
            );
          })}
        </div>

        {/* ePBS events row */}
        {epbsActive && epbsRowBottom >= 0 && (
          <div className="event-row builder-events" style={{ bottom: `${epbsRowBottom}px` }}>
            {/* Our bids */}
            {state.ourBids?.map((bid: OurBid, idx: number) => {
              const bidMs = bid.time - slotStartTime;
              const bidSuccess = bid.success !== false;
              return renderEventDot(
                bidSuccess ? 'bid-submitted' : 'bid-failed',
                bidMs,
                {
                  title: `Our Bid #${idx + 1}`,
                  items: [
                    { label: 'Time', value: `${bidMs}ms` },
                    { label: 'Bid Amount', value: formatGwei(bid.value) },
                    ...(bid.blockHash ? [{
                      label: 'Block Hash',
                      value: truncateHash(bid.blockHash),
                      copyValue: bid.blockHash
                    }] : []),
                    { label: 'Status', value: bidSuccess ? 'Success' : 'Failed' },
                    ...(bid.error ? [{ label: 'Error', value: bid.error }] : [])
                  ]
                },
                `bid-${idx}`
              );
            })}

            {/* Reveal attempts — one dot per attempt so each retry/error is visible */}
            {genesisTime > 0 && state.revealAttempts?.map((att, idx) => {
              const failed = !att.success && !att.skipped;
              const status = att.success ? 'Success' : (att.skipped ? 'Skipped' : 'Failed');
              // A skipped attempt (reveal withheld by the action plan or missed
              // deadline) never published an envelope — render it distinctly
              // instead of as a "sent" dot, which reads as a successful reveal.
              const dotClass = att.success ? 'reveal-sent' : att.skipped ? 'reveal-skipped' : 'reveal-failed';
              const title = att.attempt && att.maxAttempts
                ? `Payload Reveal (attempt ${att.attempt}/${att.maxAttempts})`
                : att.skipped ? 'Payload Reveal (withheld)' : 'Payload Reveal';
              return renderEventDot(
                dotClass,
                att.time - slotStartTime,
                {
                  title,
                  items: [
                    { label: 'Time', value: `${att.time - slotStartTime}ms` },
                    { label: 'Status', value: status },
                    ...(att.skipped && att.skipReason ? [{ label: 'Reason', value: att.skipReason }] : []),
                    ...(att.error ? [{ label: 'Error', value: att.error }] : [])
                  ]
                },
                `reveal-${idx}`
              );
            })}
          </div>
        )}

        {/* Builder API events row */}
        {builderApiActive && (
          <div className="event-row builder-api-events" style={{ bottom: `${builderApiRowBottom}px` }}>
            {/* One dot per Builder API call, positioned at the request and colored
                by outcome: green once we delivered a response, amber while a call
                was received but not (yet) answered. Both forks share this scheme;
                the popover names the call and carries both timestamps. */}

            {/* getHeader (Fulu) */}
            {state.getHeaderReceivedAt && genesisTime > 0 && renderEventDot(
              `builder-api-call ${state.getHeaderDeliveredAt ? 'delivered' : 'pending'}`,
              state.getHeaderReceivedAt - slotStartTime,
              {
                title: 'getHeader (Fulu)',
                items: [
                  { label: 'Received', value: `${state.getHeaderReceivedAt - slotStartTime}ms` },
                  state.getHeaderDeliveredAt
                    ? { label: 'Delivered', value: `${state.getHeaderDeliveredAt - slotStartTime}ms` }
                    : { label: 'Status', value: 'no response' },
                  ...(state.getHeaderBlockValue ? [{
                    label: 'Block Value',
                    value: state.getHeaderBlockValue + ' wei'
                  }] : []),
                  ...(state.getHeaderBlockHash ? [{
                    label: 'Block Hash',
                    value: truncateHash(state.getHeaderBlockHash),
                    copyValue: state.getHeaderBlockHash
                  }] : [])
                ]
              },
              'builder-api-get-header'
            )}

            {/* submitBlindedBlock (Fulu) */}
            {state.submitBlindedReceivedAt && genesisTime > 0 && renderEventDot(
              `builder-api-call ${state.submitBlindedDeliveredAt ? 'delivered' : 'pending'}`,
              state.submitBlindedReceivedAt - slotStartTime,
              {
                title: 'submitBlindedBlock (Fulu)',
                items: [
                  { label: 'Received', value: `${state.submitBlindedReceivedAt - slotStartTime}ms` },
                  state.submitBlindedDeliveredAt
                    ? { label: 'Published', value: `${state.submitBlindedDeliveredAt - slotStartTime}ms` }
                    : { label: 'Status', value: 'not published' },
                  ...(state.submitBlindedBlockHash ? [{
                    label: 'Block Hash',
                    value: truncateHash(state.submitBlindedBlockHash),
                    copyValue: state.submitBlindedBlockHash
                  }] : [])
                ]
              },
              'builder-api-submit-blinded'
            )}

            {/* getExecutionPayloadBid (Gloas) */}
            {state.getBidReceivedAt && genesisTime > 0 && renderEventDot(
              `builder-api-call ${state.getBidDeliveredAt ? 'delivered' : 'pending'}`,
              state.getBidReceivedAt - slotStartTime,
              {
                title: 'getExecutionPayloadBid (Gloas)',
                items: [
                  { label: 'Received', value: `${state.getBidReceivedAt - slotStartTime}ms` },
                  state.getBidDeliveredAt
                    ? { label: 'Delivered', value: `${state.getBidDeliveredAt - slotStartTime}ms` }
                    : { label: 'Status', value: 'no response' },
                  ...(state.getBidBlockValue ? [{
                    label: 'Bid Value',
                    value: state.getBidBlockValue + ' gwei'
                  }] : []),
                  ...(state.getBidBlockHash ? [{
                    label: 'Block Hash',
                    value: truncateHash(state.getBidBlockHash),
                    copyValue: state.getBidBlockHash
                  }] : [])
                ]
              },
              'builder-api-get-bid'
            )}

            {/* submitSignedBeaconBlock (Gloas) — proposer returns the signed block, we reveal the envelope */}
            {state.submitBlockReceivedAt && genesisTime > 0 && renderEventDot(
              `builder-api-call ${state.submitBlockDeliveredAt ? 'delivered' : 'pending'}`,
              state.submitBlockReceivedAt - slotStartTime,
              {
                title: 'submitSignedBeaconBlock (Gloas)',
                items: [
                  { label: 'Received', value: `${state.submitBlockReceivedAt - slotStartTime}ms` },
                  state.submitBlockDeliveredAt
                    ? { label: 'Revealed', value: `${state.submitBlockDeliveredAt - slotStartTime}ms` }
                    : { label: 'Status', value: 'not revealed' },
                  ...(state.submitBlockBlockHash ? [{
                    label: 'Block Hash',
                    value: truncateHash(state.submitBlockBlockHash),
                    copyValue: state.submitBlockBlockHash
                  }] : [])
                ]
              },
              'builder-api-submit-block'
            )}
          </div>
        )}

        {/* Current time indicator - animated via requestAnimationFrame */}
        <CurrentTimeIndicator
          slotStartTime={slotStartTime}
          rangeStart={rangeStart}
          totalRange={totalRange}
        />
      </div>

      {popover && (
        <Popover data={popover.data} x={popover.x} y={popover.y} onClose={closePopover}>
          {popover.headVoteSlot !== undefined && (
            <HeadVoteHeatmap slot={popover.headVoteSlot} slotDurationMs={slotDuration} />
          )}
        </Popover>
      )}
    </div>
  );
};
