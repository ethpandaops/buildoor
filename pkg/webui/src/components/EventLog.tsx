import React from 'react';
import type { LogEvent } from '../types';
import { getEventTypeClass } from '../utils';

interface EventLogProps {
  events: LogEvent[];
  onClear: () => void;
}

export const EventLog: React.FC<EventLogProps> = ({ events, onClear }) => {
  return (
    <div className="card mb-3 flex-grow-1 events-log-container">
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">Recent Events</h5>
        <button className="btn btn-sm btn-outline-secondary" onClick={onClear}>
          Clear
        </button>
      </div>
      <div className="card-body p-0 d-flex flex-column" style={{ minHeight: 0 }}>
        <div className="events-log">
          {events.slice(0, 50).map((event, idx) => (
            <div key={idx} className={`event-item ${getEventTypeClass(event.type)}`}>
              <span className="event-time">
                {new Date(event.timestamp).toLocaleTimeString()}
              </span>
              <span className="event-message">{event.message}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
};
