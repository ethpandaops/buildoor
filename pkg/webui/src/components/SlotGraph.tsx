import React, { useState, useCallback, useRef } from 'react';
import type { SlotState, Config, ChainInfo, OurBid, ExternalBid, HeadVoteDataPoint, ServiceStatus } from '../types';
import { formatGwei, isSlotScheduled, calculateSlotTiming, calculatePosition } from '../utils';
import { Popover, PopoverData } from './Popover';
import { CurrentTimeIndicator } from './CurrentTimeIndicator';

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
}

interface PopoverState {
  data: PopoverData;
  x: number;
  y: number;
}

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

  // Line: horizontal extension left -> data points -> horizontal extension right
  const parts = [
    `M ${startX} ${firstY}`,
    ...coords.map(c => `L ${c.x} ${c.y}`),
    `L ${endX} ${lastY}`
  ];
  const linePath = parts.join(' ');

  // Close via bottom for gradient fill area
  const areaPath = `${linePath} L ${endX} 100 L ${startX} 100 Z`;

  return { linePath, areaPath, startX, endX };
};

export const SlotGraph: React.FC<SlotGraphProps> = ({
  slot,
  state,
  originalConfig,
  originalServiceStatus,
  currentConfig,
  chainInfo,
  currentDisplaySlot,
  serviceStatus
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
  const revealX = epbsConfig ? calculatePosition(epbsConfig.reveal_time, rangeStart, totalRange) : 0;

  // Build delay line: from build_start_time to payloadCreatedAt
  const buildStartX = epbsConfig ? calculatePosition(epbsConfig.build_start_time, rangeStart, totalRange) : 0;
  const payloadCreatedX = state.payloadCreatedAt
    ? calculatePosition(state.payloadCreatedAt - slotStartTime, rangeStart, totalRange)
    : 0;

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

          {/* Reveal marker - spans ePBS row only */}
          {epbsActive && epbsRowBottom >= 0 && epbsConfig && revealX >= 0 && revealX <= 100 && (
            <div
              className={`reveal-marker ${!state.bidWon ? 'reveal-marker-disabled' : ''}`}
              style={{
                bottom: `${epbsRowBottom}px`,
                height: `${ROW_HEIGHT}px`,
                left: `${revealX}%`
              }}
            >
              <span className="duty-label reveal-label">Reveal: {epbsConfig.reveal_time}ms</span>
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

        {/* Chain + payload events row (always on top) */}
        <div className="event-row chain-events" style={{ bottom: `${chainRowBottom}px` }}>
          {/* Build delay line: from build_start_time to payload ready */}
          {epbsConfig && state.payloadCreatedAt && genesisTime > 0 && buildStartX < 100 && payloadCreatedX > 0 && (
            <div
              className="build-delay-line"
              style={{
                left: `${Math.max(0, buildStartX)}%`,
                width: `${Math.min(100, payloadCreatedX) - Math.max(0, buildStartX)}%`
              }}
              onClick={(e) => showPopover(e, {
                title: 'Build Delay',
                items: [
                  { label: 'Build Start', value: `${epbsConfig.build_start_time}ms` },
                  { label: 'Payload Ready', value: `${state.payloadCreatedAt! - slotStartTime}ms` },
                  { label: 'Duration', value: `${(state.payloadCreatedAt! - slotStartTime) - epbsConfig.build_start_time}ms` }
                ]
              })}
            />
          )}

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

        {/* Builder API events row */}
        {builderApiActive && (
          <div className="event-row builder-api-events" style={{ bottom: `${builderApiRowBottom}px` }}>
            {/* getHeader received */}
            {state.getHeaderReceivedAt && genesisTime > 0 && renderEventDot(
              'get-header-received',
              state.getHeaderReceivedAt - slotStartTime,
              {
                title: 'getHeader Request',
                items: [
                  { label: 'Time', value: `${state.getHeaderReceivedAt - slotStartTime}ms` }
                ]
              },
              'get-header-rcvd'
            )}

            {/* getHeader delivered */}
            {state.getHeaderDeliveredAt && genesisTime > 0 && renderEventDot(
              'get-header-delivered',
              state.getHeaderDeliveredAt - slotStartTime,
              {
                title: 'Header Delivered',
                items: [
                  { label: 'Time', value: `${state.getHeaderDeliveredAt - slotStartTime}ms` },
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
              'get-header-dlvd'
            )}

            {/* submitBlindedBlock received */}
            {state.submitBlindedReceivedAt && genesisTime > 0 && renderEventDot(
              'submit-blinded-received',
              state.submitBlindedReceivedAt - slotStartTime,
              {
                title: 'submitBlindedBlock Request',
                items: [
                  { label: 'Time', value: `${state.submitBlindedReceivedAt - slotStartTime}ms` },
                  ...(state.submitBlindedBlockHash ? [{
                    label: 'Block Hash',
                    value: truncateHash(state.submitBlindedBlockHash),
                    copyValue: state.submitBlindedBlockHash
                  }] : [])
                ]
              },
              'submit-blinded-rcvd'
            )}

            {/* submitBlindedBlock delivered */}
            {state.submitBlindedDeliveredAt && genesisTime > 0 && renderEventDot(
              'submit-blinded-delivered',
              state.submitBlindedDeliveredAt - slotStartTime,
              {
                title: 'Block Published',
                items: [
                  { label: 'Time', value: `${state.submitBlindedDeliveredAt - slotStartTime}ms` }
                ]
              },
              'submit-blinded-dlvd'
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
        <Popover data={popover.data} x={popover.x} y={popover.y} onClose={closePopover} />
      )}
    </div>
  );
};
