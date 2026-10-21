import React from 'react';
import { useEventStream } from '../hooks/useEventStream';
import { formatSlotDuration, nextSlotSegment, slotDurationAt } from '../utils';

// LiveStatus shows the event stream connection and the current slot in the
// header, on every page; on chains whose slot duration changes also the
// current duration and the next change.
export const LiveStatus: React.FC = () => {
  const { connected, currentSlot, chainInfo } = useEventStream();
  const epoch = chainInfo && currentSlot ? Math.floor(currentSlot / chainInfo.slots_per_epoch) : null;
  const durationChanges = (chainInfo?.slot_segments?.length ?? 0) > 1;
  const duration = chainInfo ? slotDurationAt(chainInfo, currentSlot) : 0;
  const next = chainInfo ? nextSlotSegment(chainInfo, currentSlot) : null;

  return (
    <span className="hdr-live font-monospace" title={connected ? 'Live: connected to the event stream' : 'Reconnecting to the event stream…'}>
      <span className={`hdr-live-dot${connected ? ' on' : ''}`} aria-hidden="true" />
      <span className="visually-hidden">{connected ? 'Connected' : 'Disconnected'}</span>
      {currentSlot > 0 && <span>slot {currentSlot.toLocaleString('en-US')}</span>}
      {epoch !== null && <span className="hdr-live-epoch">epoch {epoch.toLocaleString('en-US')}</span>}
      {durationChanges && currentSlot > 0 && (
        <span
          className="hdr-live-epoch"
          title={next
            ? `Slot duration changes to ${formatSlotDuration(next.duration_ms)} at epoch ${next.epoch.toLocaleString('en-US')} (slot ${next.slot.toLocaleString('en-US')})`
            : 'Slot duration of the current slot'}
        >
          {formatSlotDuration(duration)} slots{next ? ` → ${formatSlotDuration(next.duration_ms)} @ epoch ${next.epoch.toLocaleString('en-US')}` : ''}
        </span>
      )}
    </span>
  );
};
