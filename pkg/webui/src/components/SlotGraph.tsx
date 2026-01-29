import React, { useState, useCallback, useRef } from 'react';
import type { SlotState, Config, ChainInfo, OurBid, ExternalBid, HeadVoteDataPoint, LegacySubmission, ServiceStatus, LegacyBuilderInfo as LegacyBuilderInfoType } from '../types';
import { formatGwei, isSlotScheduled, calculateSlotTiming, calculatePosition } from '../utils';
import { Popover, PopoverData } from './Popover';
import { CurrentTimeIndicator } from './CurrentTimeIndicator';

interface SlotGraphProps {
  slot: number;
  state: SlotState;
  originalConfig: Config | null;
  currentConfig: Config | null;
  chainInfo: ChainInfo | null;
  currentDisplaySlot: number;
  serviceStatus: ServiceStatus | null;
  legacyBuilderInfo: LegacyBuilderInfoType | null;
}

interface PopoverState {
  data: PopoverData;
  x: number;
  y: number;
}

const ROW_HEIGHT = 22;
// Pad in px above/below the rows. Kept small so 2 rows ~50px total.
const ROW_PAD = 3;

// Extension in viewBox units (~50px at typical container widths).
const HEAD_VOTES_EXTEND = 5;

// Maps participation percentage (0-100) to SVG Y coordinate (100=bottom, 0=top).
const pctToY = (pct: number): number => {
  return 100 - Math.max(0, Math.min(100, pct));
};

// Builds extended SVG paths with horizontal extensions that fade at the edges.
const buildHeadVotesPaths = (
  points: HeadVoteDataPoint[],
  slotStartTime: number,
  rangeStart: number,
  totalRange: number
): { linePath: string; areaPath: string; startX: number; endX: number } | null => {
  if (points.length === 0) return null;

  const coords = points.map(p => ({
    x: ((p.time - slotStartTime - rangeStart) / totalRange) * 100,
    y: pctToY(p.pct)
  }));

  const firstY = coords[0].y;
  const lastY = coords[coords.length - 1].y;
  const startX = coords[0].x - HEAD_VOTES_EXTEND;
  const endX = coords[coords.length - 1].x + HEAD_VOTES_EXTEND;

  const parts = [
    `M ${startX} ${firstY}`,
    ...coords.map(c => `L ${c.x} ${c.y}`),
    `L ${endX} ${lastY}`
  ];
  const linePath = parts.join(' ');
  const areaPath = `${linePath} L ${endX} 100 L ${startX} 100 Z`;

  return { linePath, areaPath, startX, endX };
};

export const SlotGraph: React.FC<SlotGraphProps> = ({
  slot,
  state,
  originalConfig,
  currentConfig,
  chainInfo,
  currentDisplaySlot,
  serviceStatus,
  legacyBuilderInfo
}) => {
  const [popover, setPopover] = useState<PopoverState | null>(null);

  const capturedConfigRef = useRef<Config | null>(null);
  if (capturedConfigRef.current === null) {
    capturedConfigRef.current = originalConfig || currentConfig;
  }

  // Freeze service enablement/timings per-rendered slot so toggling services doesn't
  // change graphs that are already on-screen (current / next / past).
  const capturedServiceStatusRef = useRef<ServiceStatus | null>(null);
  if (capturedServiceStatusRef.current === null && serviceStatus !== null) {
    capturedServiceStatusRef.current = serviceStatus;
  }
  const capturedLegacyInfoRef = useRef<LegacyBuilderInfoType | null>(null);
  if (capturedLegacyInfoRef.current === null && legacyBuilderInfo !== null) {
    capturedLegacyInfoRef.current = legacyBuilderInfo;
  }

  const { slotStartTime, rangeStart, totalRange } = calculateSlotTiming(chainInfo, slot);
  const slotDuration = chainInfo?.seconds_per_slot || 12000;
  const genesisTime = chainInfo?.genesis_time || 0;

  const isSlotInFuture = slot > currentDisplaySlot;
  const config = isSlotInFuture ? currentConfig : capturedConfigRef.current;

  const epbsConfig = config?.epbs;
  const isScheduled = state.scheduled !== undefined ? state.scheduled : isSlotScheduled(slot, config?.schedule);

  // Determine which rows are active
  const status = capturedServiceStatusRef.current;
  const legacyInfo = capturedLegacyInfoRef.current;

  const epbsRowActive = (status?.epbs_available ?? false) && (status?.epbs_enabled ?? false);
  const legacyRowActive = (status?.legacy_available ?? false) && (status?.legacy_enabled ?? false);
  const activeRows = 1 + (epbsRowActive ? 1 : 0) + (legacyRowActive ? 1 : 0);

  // Calculate row positions (from bottom)
  let rowOffset = ROW_PAD;

  let legacyRowBottom: number | undefined;
  if (legacyRowActive) {
    legacyRowBottom = rowOffset;
    rowOffset += ROW_HEIGHT;
  }

  let epbsRowBottom: number | undefined;
  if (epbsRowActive) {
    epbsRowBottom = rowOffset;
    rowOffset += ROW_HEIGHT;
  }

  const chainRowBottom = rowOffset;

  // Graph height is exactly the number of rendered rows.
  const graphHeight = activeRows * ROW_HEIGHT + 2 * ROW_PAD;

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

  // ePBS bid window and reveal marker
  const bidStartX = epbsConfig ? calculatePosition(epbsConfig.bid_start_time, rangeStart, totalRange) : 0;
  const bidEndX = epbsConfig ? calculatePosition(epbsConfig.bid_end_time, rangeStart, totalRange) : 0;
  const revealX = epbsConfig ? calculatePosition(epbsConfig.reveal_time, rangeStart, totalRange) : 0;

  // Legacy submit window
  const legacySubmitStartX = legacyInfo ? calculatePosition(legacyInfo.submit_start_time, rangeStart, totalRange) : 0;
  const legacySubmitEndX = legacyInfo ? calculatePosition(legacyInfo.submit_end_time, rangeStart, totalRange) : 0;

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
      <div className="slot-graph-label">{slotLabel}</div>
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

          {/* ePBS Bid window - spans the ePBS row */}
          {epbsRowActive && epbsConfig && bidStartX < 100 && bidEndX > 0 && epbsRowBottom !== undefined && (
            <div
              className={`bid-window-bg ${!isScheduled ? 'bid-window-disabled' : ''}`}
              style={{
                left: `${Math.max(0, bidStartX)}%`,
                width: `${Math.min(100, bidEndX) - Math.max(0, bidStartX)}%`,
                bottom: `${epbsRowBottom}px`,
                height: `${ROW_HEIGHT}px`
              }}
            >
              <span className="duty-label bid-label">
                Bid: {epbsConfig.bid_start_time} - {epbsConfig.bid_end_time}ms
              </span>
            </div>
          )}

          {/* ePBS Reveal marker - spans the ePBS row */}
          {epbsRowActive && epbsConfig && revealX >= 0 && revealX <= 100 && epbsRowBottom !== undefined && (
            <div
              className={`reveal-marker ${!state.bidWon ? 'reveal-marker-disabled' : ''}`}
              style={{
                left: `${revealX}%`,
                bottom: `${epbsRowBottom}px`,
                height: `${ROW_HEIGHT}px`
              }}
            >
              <span className="duty-label reveal-label">Reveal: {epbsConfig.reveal_time}ms</span>
            </div>
          )}

          {/* Legacy submit window - spans the legacy row */}
          {legacyRowActive && legacyInfo && legacySubmitStartX < 100 && legacySubmitEndX > 0 && legacyRowBottom !== undefined && (
            <div
              className="submit-window-bg"
              style={{
                left: `${Math.max(0, legacySubmitStartX)}%`,
                width: `${Math.min(100, legacySubmitEndX) - Math.max(0, legacySubmitStartX)}%`,
                bottom: `${legacyRowBottom}px`,
                height: `${ROW_HEIGHT}px`
              }}
            >
              <span className="duty-label submit-label">
                Submit: {legacyInfo.submit_start_time} - {legacyInfo.submit_end_time}ms
              </span>
            </div>
          )}
        </div>

        {/* Head votes participation graph */}
        {state.headVotes && state.headVotes.length > 0 && (() => {
          const paths = buildHeadVotesPaths(state.headVotes, slotStartTime, rangeStart, totalRange);
          if (!paths) return null;

          const { linePath, areaPath, startX, endX } = paths;
          const totalWidth = endX - startX;
          const fadeIn = `${(HEAD_VOTES_EXTEND / totalWidth) * 100}%`;
          const fadeOut = `${(1 - HEAD_VOTES_EXTEND / totalWidth) * 100}%`;
          const maxVote = state.headVotes.reduce((best, p) => p.pct > best.pct ? p : best, state.headVotes[0]);

          return (
            <svg
              className="head-votes-graph"
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              style={{ overflow: 'visible' }}
            >
              <defs>
                <linearGradient id={`hvg-${slot}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="rgba(0, 188, 212, 0.3)" />
                  <stop offset="100%" stopColor="rgba(0, 188, 212, 0.05)" />
                </linearGradient>
                <linearGradient
                  id={`hvfade-${slot}`}
                  gradientUnits="userSpaceOnUse"
                  x1={String(startX)} y1="0" x2={String(endX)} y2="0"
                >
                  <stop offset="0%" stopColor="white" stopOpacity="0" />
                  <stop offset={fadeIn} stopColor="white" stopOpacity="1" />
                  <stop offset={fadeOut} stopColor="white" stopOpacity="1" />
                  <stop offset="100%" stopColor="white" stopOpacity="0" />
                </linearGradient>
                <mask id={`hvmask-${slot}`}>
                  <rect x={startX} y="0" width={totalWidth} height="100" fill={`url(#hvfade-${slot})`} />
                </mask>
              </defs>
              <g mask={`url(#hvmask-${slot})`}>
                <path d={areaPath} fill={`url(#hvg-${slot})`} />
                <path d={linePath} fill="none" stroke="#00bcd4" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
              </g>
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
                      items: [
                        { label: 'Max Participation', value: `${maxVote.pct.toFixed(1)}%` },
                        { label: 'Participating ETH', value: `${maxVote.eth.toLocaleString()} ETH` }
                      ]
                    },
                    x: Math.min(e.clientX, window.innerWidth - 330),
                    y: e.clientY + 10
                  });
                }}
              />
            </svg>
          );
        })()}

        {/* Chain events row */}
        <div className="event-row" style={{ bottom: `${chainRowBottom}px` }}>
          {/* Block received */}
          {state.blockReceivedAt && genesisTime > 0 && renderEventDot(
            'block-received',
            state.blockReceivedAt - slotStartTime,
            {
              title: 'Block Received',
              items: [
                { label: 'Time', value: `${state.blockReceivedAt - slotStartTime}ms` },
                ...(state.blockRoot ? [{
                  label: 'Block Root',
                  value: truncateHash(state.blockRoot),
                  copyValue: state.blockRoot
                }] : [])
              ]
            },
            'block'
          )}

          {/* Payload envelope */}
          {state.payloadEnvelopeAt && genesisTime > 0 && renderEventDot(
            'payload-envelope',
            state.payloadEnvelopeAt - slotStartTime,
            {
              title: 'Payload Envelope',
              items: [
                { label: 'Time', value: `${state.payloadEnvelopeAt - slotStartTime}ms` },
                ...(state.payloadEnvelopeBlockHash ? [{
                  label: 'Payload Hash',
                  value: truncateHash(state.payloadEnvelopeBlockHash),
                  copyValue: state.payloadEnvelopeBlockHash
                }] : []),
                ...(state.payloadEnvelopeBuilder !== undefined ? [{
                  label: 'Builder Index',
                  value: String(state.payloadEnvelopeBuilder)
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

        {/* ePBS Builder events row */}
        {epbsRowActive && epbsRowBottom !== undefined && (
          <div className="event-row" style={{ bottom: `${epbsRowBottom}px` }}>
            {/* Build delay indicator (line from build request to payload ready) */}
            {state.buildRequestedAt && state.payloadCreatedAt && genesisTime > 0 && (() => {
              const buildStartMs = state.buildRequestedAt - slotStartTime;
              const buildEndMs = state.payloadCreatedAt - slotStartTime;
              const startX = calculatePosition(buildStartMs, rangeStart, totalRange);
              const endX = calculatePosition(buildEndMs, rangeStart, totalRange);
              const buildDuration = buildEndMs - buildStartMs;
              if (startX > 100 || endX < 0) return null;
              const clampedStartX = Math.max(0, Math.min(100, startX));
              const clampedEndX = Math.max(0, Math.min(100, endX));
              const width = clampedEndX - clampedStartX;
              if (width <= 0) return null;
              return (
                <div
                  className="build-delay-line"
                  style={{
                    left: `${clampedStartX}%`,
                    width: `${width}%`
                  }}
                  onClick={(e) => showPopover(e, {
                    title: 'Build Delay',
                    items: [
                      { label: 'FCU Sent', value: `${buildStartMs}ms` },
                      { label: 'Payload Ready', value: `${buildEndMs}ms` },
                      { label: 'Build Duration', value: `${buildDuration}ms` }
                    ]
                  })}
                />
              );
            })()}

            {/* Payload created */}
            {state.payloadCreatedAt && genesisTime > 0 && renderEventDot(
              'payload-created',
              state.payloadCreatedAt - slotStartTime,
              {
                title: 'Payload Created',
                items: [
                  { label: 'Time', value: `${state.payloadCreatedAt - slotStartTime}ms` },
                  ...(state.buildRequestedAt ? [{
                    label: 'Build Duration',
                    value: `${state.payloadCreatedAt - state.buildRequestedAt}ms`
                  }] : []),
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

            {/* Reveal */}
            {state.revealSentAt && genesisTime > 0 && renderEventDot(
              state.revealFailed ? 'reveal-failed' : 'reveal-sent',
              state.revealSentAt - slotStartTime,
              {
                title: 'Payload Reveal',
                items: [
                  { label: 'Time', value: `${state.revealSentAt - slotStartTime}ms` },
                  { label: 'Status', value: state.revealFailed ? 'Failed' : (state.revealSkipped ? 'Skipped' : 'Success') }
                ]
              },
              'reveal'
            )}
          </div>
        )}

        {/* Legacy PBS events row */}
        {legacyRowActive && legacyRowBottom !== undefined && (
          <div className="event-row" style={{ bottom: `${legacyRowBottom}px` }}>
            {state.legacySubmissions?.map((sub: LegacySubmission, idx: number) => {
              const subMs = sub.time - slotStartTime;
              return renderEventDot(
                sub.success ? 'legacy-submitted' : 'legacy-failed',
                subMs,
                {
                  title: `Legacy Submission #${idx + 1}`,
                  items: [
                    { label: 'Time', value: `${subMs}ms` },
                    { label: 'Relay', value: sub.relayUrl },
                    { label: 'Value', value: `${sub.value} wei` },
                    ...(sub.blockHash ? [{
                      label: 'Block Hash',
                      value: truncateHash(sub.blockHash),
                      copyValue: sub.blockHash
                    }] : []),
                    { label: 'Status', value: sub.success ? 'Accepted' : 'Failed' },
                    ...(sub.error ? [{ label: 'Error', value: sub.error }] : [])
                  ]
                },
                `legacy-${idx}`
              );
            })}
          </div>
        )}

        {/* Current time indicator */}
        <CurrentTimeIndicator
          slotStartTime={slotStartTime}
          rangeStart={rangeStart}
          totalRange={totalRange}
        />
      </div>

      {popover && (
        <Popover data={popover.data} x={popover.x} y={popover.y} onClose={closePopover} />
      )}
    </div>
  );
};
