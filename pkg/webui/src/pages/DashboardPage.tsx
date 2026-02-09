import React from 'react';
import { useEventStream } from '../hooks/useEventStream';
import { useBuilderAPIStatus } from '../hooks/useBuilderAPIStatus';
import { SlotTimeline } from '../components/SlotTimeline';
import { Legend } from '../components/Legend';
import { EventLog } from '../components/EventLog';
import { ConfigPanel } from '../components/ConfigPanel';
import { BuilderInfo } from '../components/BuilderInfo';
import { BuilderConfigPanel } from '../components/BuilderConfigPanel';
import { BuilderAPIConfigPanel } from '../components/BuilderAPIConfigPanel';

const DashboardPage: React.FC = () => {
  const {
    connected,
    config,
    chainInfo,
    stats,
    builderInfo,
    serviceStatus,
    currentSlot,
    slotStates,
    slotConfigs,
    slotServiceStatuses,
    events,
    clearEvents
  } = useEventStream();

  const { status: builderAPIStatus, loading: builderAPIStatusLoading } = useBuilderAPIStatus();

  return (
    <div className="d-flex flex-column" style={{ height: 'calc(100vh - 120px)' }}>
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
                slotServiceStatuses={slotServiceStatuses}
                currentConfig={config}
                serviceStatus={serviceStatus}
              />
              <Legend />
            </div>
          </div>

          {/* Events Log */}
          <EventLog events={events} onClear={clearEvents} />
        </div>

        {/* Right column: Builder Info, Config and Panels */}
        <div className="col-lg-4">
          {/* Builder Info */}
          <BuilderInfo builderInfo={builderInfo} />

          {/* Payload Builder */}
          <BuilderConfigPanel config={config} />

          {/* ePBS Bidder */}
          <ConfigPanel config={config} serviceStatus={serviceStatus} stats={stats} />

          {/* Builder API (Legacy) */}
          <BuilderAPIConfigPanel status={builderAPIStatus} serviceStatus={serviceStatus} loading={builderAPIStatusLoading} />
        </div>
      </div>
    </div>
  );
};

export default DashboardPage;
