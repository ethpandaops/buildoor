import React from 'react';
import { useEventStream } from '../hooks/useEventStream';

// LiveStatus shows the event stream connection and the current slot in the
// header, on every page.
export const LiveStatus: React.FC = () => {
  const { connected, currentSlot, chainInfo } = useEventStream();
  const epoch = chainInfo && currentSlot ? Math.floor(currentSlot / chainInfo.slots_per_epoch) : null;

  return (
    <span className="hdr-live font-monospace" title={connected ? 'Live: connected to the event stream' : 'Reconnecting to the event stream…'}>
      <span className={`hdr-live-dot${connected ? ' on' : ''}`} aria-hidden="true" />
      <span className="visually-hidden">{connected ? 'Connected' : 'Disconnected'}</span>
      {currentSlot > 0 && <span>slot {currentSlot.toLocaleString('en-US')}</span>}
      {epoch !== null && <span className="hdr-live-epoch">epoch {epoch.toLocaleString('en-US')}</span>}
    </span>
  );
};
