import React from 'react';
import { useEventStream } from './hooks/useEventStream';
import { SlotTimeline } from './components/SlotTimeline';
import { Legend } from './components/Legend';
import { EventLog } from './components/EventLog';
import { Stats } from './components/Stats';
import { ConfigPanel } from './components/ConfigPanel';
import { BuilderInfo } from './components/BuilderInfo';

export const App: React.FC = () => {
  const {
    connected,
    config,
    chainInfo,
    stats,
    builderInfo,
    currentSlot,
    slotStates,
    slotConfigs,
    events,
    clearEvents
  } = useEventStream();

  return (
    <div className="container-fluid mt-2 d-flex flex-column" style={{ height: 'calc(100vh - 80px)' }}>
      <div className="row flex-grow-1" style={{ minHeight: 0 }}>
        {/* Left column: Timeline visualization */}
        <div className="col-lg-8 d-flex flex-column">
          <div className="card mb-3">
            <div className="card-header d-flex justify-content-between align-items-center">
              <h5 className="mb-0">Slot Timeline</h5>
              <div className="d-flex gap-2">
                <span className={`badge ${connected ? 'bg-success' : 'bg-danger'}`}>
                  {connected ? 'Connected' : 'Disconnected'}
                </span>
                <span className="badge bg-primary">Slot: {currentSlot || '--'}</span>
              </div>
            </div>
            <div className="card-body p-2">
              <SlotTimeline
                chainInfo={chainInfo}
                slotStates={slotStates}
                slotConfigs={slotConfigs}
                currentConfig={config}
              />
              <Legend />
            </div>
          </div>

          {/* Events Log */}
          <EventLog events={events} onClear={clearEvents} />
        </div>

        {/* Right column: Builder Info, Config and Stats */}
        <div className="col-lg-4">
          {/* Builder Info */}
          <BuilderInfo builderInfo={builderInfo} />

          {/* Stats */}
          <Stats stats={stats} />

          {/* Config panels */}
          <ConfigPanel config={config} />
        </div>
      </div>
    </div>
  );
};
