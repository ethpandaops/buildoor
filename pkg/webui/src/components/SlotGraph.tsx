import React, { useState, useCallback, useRef } from 'react';
import type { SlotState, Config, ChainInfo, OurBid, ExternalBid } from '../types';
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
}

interface PopoverState {
  data: PopoverData;
  x: number;
  y: number;
}

export const SlotGraph: React.FC<SlotGraphProps> = ({
  slot,
  state,
  originalConfig,
  currentConfig,
  chainInfo,
  currentDisplaySlot
}) => {
  const [popover, setPopover] = useState<PopoverState | null>(null);

  // Capture config on FIRST RENDER - never change after that
  const capturedConfigRef = useRef<Config | null>(null);
  if (capturedConfigRef.current === null) {
    capturedConfigRef.current = originalConfig || currentConfig;
  }

  const { slotStartTime, rangeStart, totalRange } = calculateSlotTiming(chainInfo, slot);
  const slotDuration = chainInfo?.seconds_per_slot || 12000;
  const genesisTime = chainInfo?.genesis_time || 0;

  // Only use currentConfig for FUTURE slots, otherwise use captured config
  const isSlotInFuture = slot > currentDisplaySlot;
  const config = isSlotInFuture ? currentConfig : capturedConfigRef.current;

  const epbsConfig = config?.epbs;
  const isScheduled = state.scheduled !== undefined ? state.scheduled : isSlotScheduled(slot, config?.schedule);

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
    <div className="slot-graph" onClick={closePopover}>
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

          {/* Bid window */}
          {epbsConfig && bidStartX < 100 && bidEndX > 0 && (
            <div
              className={`bid-window-bg ${!isScheduled ? 'bid-window-disabled' : ''}`}
              style={{
                left: `${Math.max(0, bidStartX)}%`,
                width: `${Math.min(100, bidEndX) - Math.max(0, bidStartX)}%`
              }}
            >
              <span className="duty-label bid-label">
                Bid: {epbsConfig.bid_start_time} - {epbsConfig.bid_end_time}ms
              </span>
            </div>
          )}

          {/* Reveal marker */}
          {epbsConfig && revealX >= 0 && revealX <= 100 && (
            <div
              className={`reveal-marker ${!state.bidWon ? 'reveal-marker-disabled' : ''}`}
              style={{ left: `${revealX}%` }}
            >
              <span className="duty-label reveal-label">Reveal: {epbsConfig.reveal_time}ms</span>
            </div>
          )}
        </div>

        {/* Chain events row */}
        <div className="event-row chain-events">
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

        {/* Builder events row */}
        <div className="event-row builder-events">
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
