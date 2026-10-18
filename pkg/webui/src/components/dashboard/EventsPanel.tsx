import React, { useState } from 'react';
import type { LogEvent } from '../../types';
import { formatEventTime, getEventTypeClass } from '../../utils';
import { EventLog } from '../EventLog';
import { count } from './format';

interface EventsPanelProps {
  events: LogEvent[];
  onClear: () => void;
}

const STORAGE_KEY = 'buildoor.dashboard.events-open';

function storedOpen(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === '1';
  } catch {
    return false;
  }
}

// EventsPanel folds the event log into one line (the latest event and the
// error count) between the timeline and the flow cards; expanded it is the
// full filterable log at a fixed height, so it never resizes the page while
// events stream in.
export const EventsPanel: React.FC<EventsPanelProps> = ({ events, onClear }) => {
  const [open, setOpenState] = useState(storedOpen);
  const setOpen = (next: boolean) => {
    setOpenState(next);
    try {
      localStorage.setItem(STORAGE_KEY, next ? '1' : '0');
    } catch {
      // Not persisted (private window); the panel still toggles.
    }
  };

  if (open) {
    return <EventLog events={events} onClear={onClear} height={440} onCollapse={() => setOpen(false)} />;
  }

  const latest = events[0];
  const errors = events.filter((e) => getEventTypeClass(e.type) === 'event-error').length;

  return (
    <section className="card db-events" aria-label="Events">
      <button type="button" className="db-events-toggle" aria-expanded="false" onClick={() => setOpen(true)}>
        <i className="fas fa-chevron-right" aria-hidden="true"></i>
        Events
      </button>
      {latest ? (
        <span className="db-events-latest font-monospace">
          <span className="bd-faint">{formatEventTime(latest.timestamp)}</span>{' '}
          <span className={getEventTypeClass(latest.type)}>{latest.type}</span> {latest.message}
        </span>
      ) : (
        <span className="db-events-latest bd-muted">No events yet</span>
      )}
      <span className="bd-tag bd-tone-neutral">{count(events.length)} events</span>
      {errors > 0 && <span className="bd-tag bd-tone-danger">{count(errors)} errors</span>}
    </section>
  );
};
