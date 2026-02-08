import React, { useState } from 'react';
import { useEventStream } from './hooks/useEventStream';
import { useValidators } from './hooks/useValidators';
import { useBuilderAPIStatus } from './hooks/useBuilderAPIStatus';
import { SlotTimeline } from './components/SlotTimeline';
import { Legend } from './components/Legend';
import { EventLog } from './components/EventLog';
import { ConfigPanel } from './components/ConfigPanel';
import { BuilderInfo } from './components/BuilderInfo';
import { BuilderConfigPanel } from './components/BuilderConfigPanel';
import { BuilderAPIConfigPanel } from './components/BuilderAPIConfigPanel';
import { ValidatorList } from './components/ValidatorList';
import { BidsWonView } from './components/BidsWonView';

type ViewType = 'dashboard' | 'validators' | 'bids-won';

export const App: React.FC = () => {
  const [currentView, setCurrentView] = useState<ViewType>('dashboard');
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
    events,
    clearEvents
  } = useEventStream();

  const { validators, loading: validatorsLoading } = useValidators();
  const { status: builderAPIStatus, loading: builderAPIStatusLoading } = useBuilderAPIStatus();

  return (
    <div className="container-fluid mt-2">
      {/* Navigation Tabs */}
      <ul className="nav nav-tabs mb-3">
        <li className="nav-item">
          <button
            className={`nav-link ${currentView === 'dashboard' ? 'active' : ''}`}
            onClick={() => setCurrentView('dashboard')}
          >
            Dashboard
          </button>
        </li>
        <li className="nav-item">
          <button
            className={`nav-link ${currentView === 'validators' ? 'active' : ''}`}
            onClick={() => setCurrentView('validators')}
          >
            Validators
          </button>
        </li>
        <li className="nav-item">
          <button
            className={`nav-link ${currentView === 'bids-won' ? 'active' : ''}`}
            onClick={() => setCurrentView('bids-won')}
          >
            Bids Won
          </button>
        </li>
      </ul>

      {/* Conditional View Rendering */}
      {currentView === 'dashboard' ? (
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

              {/* Builder Config Panel */}
              <BuilderConfigPanel config={config} />

              {/* Builder API Config Panel */}
              <BuilderAPIConfigPanel status={builderAPIStatus} loading={builderAPIStatusLoading} />

              {/* Config panels */}
              <ConfigPanel config={config} serviceStatus={serviceStatus} stats={stats} />
            </div>
          </div>
        </div>
      ) : currentView === 'validators' ? (
        <ValidatorList validators={validators} loading={validatorsLoading} fullPage={true} />
      ) : (
        <BidsWonView />
      )}
    </div>
  );
};
