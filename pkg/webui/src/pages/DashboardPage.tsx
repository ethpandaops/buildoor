import React, { useRef, useState, useLayoutEffect } from 'react';
import { useEventStream } from '../hooks/useEventStream';
import { useBuilderAPIStatus } from '../hooks/useBuilderAPIStatus';
import { SlotTimeline } from '../components/SlotTimeline';
import { Legend } from '../components/Legend';
import { EventLog } from '../components/EventLog';
import { ConfigPanel } from '../components/ConfigPanel';
import { BuilderInfo } from '../components/BuilderInfo';
import { BuilderConfigPanel } from '../components/BuilderConfigPanel';
import { BuilderAPIConfigPanel } from '../components/BuilderAPIConfigPanel';
import { StatsPanel } from '../components/StatsPanel';

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

  // The events panel must scroll internally rather than expand the page. A pure
  // CSS flex chain can't express "fill remaining viewport height, OR match the
  // right column when it is taller, but never grow from my own content", so we
  // measure it: the events panel height = max(remaining viewport, right column
  // height) - the timeline card above it. Recomputed when the right column
  // (collapsible panels) or the timeline card resize, or the window resizes.
  const rowRef = useRef<HTMLDivElement>(null);
  const timelineRef = useRef<HTMLDivElement>(null);
  const rightColRef = useRef<HTMLDivElement>(null);
  const [eventsHeight, setEventsHeight] = useState(500);

  useLayoutEffect(() => {
    const compute = () => {
      const row = rowRef.current;
      const timeline = timelineRef.current;
      const rightCol = rightColRef.current;
      if (!row || !timeline) return;

      // Clamp the top so a scrolled-down page (negative top) doesn't inflate the
      // viewport estimate; when scrolled, the right column height dominates anyway.
      const rowTop = Math.max(0, row.getBoundingClientRect().top);
      const viewportAvail = window.innerHeight - rowTop;
      const rightH = rightCol ? rightCol.offsetHeight : 0;
      const targetRowH = Math.max(viewportAvail, rightH);

      // timeline card height + its bottom margin + the events card bottom margin.
      const consumed = timeline.offsetHeight + 16 + 16;
      setEventsHeight(Math.max(500, Math.floor(targetRowH - consumed)));
    };

    compute();

    const ro = new ResizeObserver(compute);
    if (timelineRef.current) ro.observe(timelineRef.current);
    if (rightColRef.current) ro.observe(rightColRef.current);
    window.addEventListener('resize', compute);

    return () => {
      ro.disconnect();
      window.removeEventListener('resize', compute);
    };
  }, []);

  return (
    <div className="d-flex flex-column" style={{ minHeight: 'calc(100vh - 120px)' }}>
      <div ref={rowRef} className="row flex-grow-1" style={{ minHeight: 0 }}>
        {/* Left column: Timeline visualization */}
        <div className="col-lg-8 d-flex flex-column">
          <div ref={timelineRef} className="card mb-3">
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
          <EventLog events={events} onClear={clearEvents} height={eventsHeight} />
        </div>

        {/* Right column: Builder Info, Config and Panels.
            align-self-start keeps it at its content height (not stretched to the
            row), so measuring its height to size the events panel can't feed back. */}
        <div className="col-lg-4 align-self-start" ref={rightColRef}>
          {/* Builder Info */}
          <BuilderInfo builderInfo={builderInfo} serviceStatus={serviceStatus} config={config} />

          {/* Statistics */}
          <StatsPanel stats={stats} serviceStatus={serviceStatus} />

          {/* Payload Builder */}
          <BuilderConfigPanel config={config} />

          {/* ePBS Bidder */}
          <ConfigPanel config={config} serviceStatus={serviceStatus} />

          {/* Builder API */}
          <BuilderAPIConfigPanel status={builderAPIStatus} serviceStatus={serviceStatus} loading={builderAPIStatusLoading} />
        </div>
      </div>
    </div>
  );
};

export default DashboardPage;
