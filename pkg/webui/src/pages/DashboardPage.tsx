import React from 'react';
import { useEventStream } from '../hooks/useEventStream';
import { StatsRow } from '../components/dashboard/StatsRow';
import { TimelineCard } from '../components/dashboard/TimelineCard';
import { BuilderCard } from '../components/dashboard/BuilderCard';
import { NextSlotCard } from '../components/dashboard/NextSlotCard';
import { EventsPanel } from '../components/dashboard/EventsPanel';
import { FlowCard } from '../components/settings/FlowCard';
import { useFlowContext } from '../components/settings/useFlowContext';
import { FLOWS, FLOW_ORDER } from '../settings/flows';
import '../styles/dashboard.css';
import '../styles/settings.css';

// The operator dashboard: headline stats, the slot timeline with the builder
// and the next slot beside it, the event log folded in between, and one card
// per flow with its switch and its configuration. Every value is public;
// switches and the Configure drawer's edits need a login.
const DashboardPage: React.FC = () => {
  const {
    config,
    chainInfo,
    stats,
    builderInfo,
    serviceStatus,
    voteCoverage,
    slotOutlook,
    txPoolStats,
    slotStates,
    slotConfigs,
    slotServiceStatuses,
    events,
    clearEvents,
  } = useEventStream();
  const flowCtx = useFlowContext();

  return (
    <div className="db-page">
      <StatsRow stats={stats} builderInfo={builderInfo} serviceStatus={serviceStatus} txPoolStats={txPoolStats} />

      <div className="db-main">
        <TimelineCard
          chainInfo={chainInfo}
          slotStates={slotStates}
          slotConfigs={slotConfigs}
          slotServiceStatuses={slotServiceStatuses}
          config={config}
          serviceStatus={serviceStatus}
          voteCoverage={voteCoverage}
        />
        <div className="db-side">
          <BuilderCard builderInfo={builderInfo} serviceStatus={serviceStatus} />
          <NextSlotCard outlook={slotOutlook} />
        </div>
      </div>

      <EventsPanel events={events} onClear={clearEvents} />

      <section aria-labelledby="db-flows-title" className="db-flows">
        <div className="db-flows-head">
          <h2 className="db-flows-title" id="db-flows-title">
            Flows &amp; configuration
          </h2>
          <span className="bd-muted small">Global baseline; per-slot action plans override it.</span>
          <span className="db-flows-legend">
            <span>
              <span className="bd-src bd-src-cli">cli</span> set by flag
            </span>
            <span>
              <span className="bd-src bd-src-ui">ui</span> changed in the UI
            </span>
            <span>untagged = default</span>
          </span>
        </div>
        <div className="db-flow-grid">
          {FLOW_ORDER.map((id) => (
            <FlowCard key={id} flow={FLOWS[id]} ctx={flowCtx} />
          ))}
        </div>
      </section>
    </div>
  );
};

export default DashboardPage;
