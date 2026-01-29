import React from 'react';
import { useEventStream } from './hooks/useEventStream';
import { SlotTimeline } from './components/SlotTimeline';
import { Legend } from './components/Legend';
import { EventLog } from './components/EventLog';
import { ConfigPanel } from './components/ConfigPanel';
import { BuilderInfo } from './components/BuilderInfo';
import { LegacyBuilderInfo } from './components/LegacyBuilderInfo';

export const App: React.FC = () => {
  const {
    connected,
    config,
    chainInfo,
    stats,
    builderInfo,
    legacyBuilderInfo,
    serviceStatus,
    currentSlot,
    slotStates,
    slotConfigs,
    events,
    clearEvents
  } = useEventStream();

  return (
    <div className="container-fluid mt-2 d-flex flex-column" style={{ minHeight: 'calc(100vh - 150px)' }}>
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

        {/* Right column: Builder Info, ePBS, Legacy PBS */}
        <div className="col-lg-4">
          <BuilderInfo builderInfo={builderInfo} />
          <ConfigPanel config={config} serviceStatus={serviceStatus} stats={stats} />
          <LegacyBuilderInfo legacyBuilderInfo={legacyBuilderInfo} serviceStatus={serviceStatus} />
        </div>
      </div>
    </div>
  );
};
