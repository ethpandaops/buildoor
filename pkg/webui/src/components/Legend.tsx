import React from 'react';

// Two-row legend: chain/network/voting context on top, our own
// bidder / Builder API events below.
const LegendView: React.FC = () => {
  return (
    <div className="mt-2">
      <div className="timeline-legend d-flex flex-wrap gap-2 small">
        <span className="legend-section">Chain:</span>
        <span><span className="legend-dot bg-block-received"></span> Block</span>
        <span><i className="fas fa-crown legend-crown" aria-hidden="true"></i> Won by us</span>
        <span><span className="legend-dot bg-payload-received"></span> Payload Envelope</span>
        <span><span className="legend-dot bg-external-bid"></span> External Bid</span>
        <span className="legend-section ms-2">Builder:</span>
        <span><span className="legend-dot bg-payload-attributes"></span> Payload Attributes</span>
        <span><span className="legend-line legend-line-build"></span> Build</span>
        <span><span className="legend-dot bg-payload-created"></span> Engine Payload</span>
        <span><span className="legend-dot bg-local-build"></span> Testing Payload</span>
        <span><span className="legend-dot bg-local-build legend-dot-selected"></span> Current pick</span>
        <span><span className="legend-dot bg-local-build-skipped"></span> Testing Build Skipped</span>
        <span><span className="legend-dot bg-build-failed"></span> Build Failed</span>
        <span className="legend-section ms-2">Voting:</span>
        <span><span className="legend-line legend-line-head-votes"></span> Head Votes</span>
        <span><span className="legend-dot bg-vote-threshold-met"></span> Threshold Met</span>
      </div>
      <div className="timeline-legend d-flex flex-wrap gap-2 small mt-1">
        <span className="legend-section">Candidates:</span>
        <span><span className="legend-line" style={{ backgroundColor: '#61e392' }}></span> parent (full)</span>
        <span><span className="legend-line" style={{ backgroundColor: '#e6a23c' }}></span> parent (empty)</span>
        <span><span className="legend-line" style={{ backgroundColor: '#a78bfa' }}></span> grandparent (full)</span>
        <span><span className="legend-line" style={{ backgroundColor: '#8a8f98' }}></span> grandparent (empty)</span>
        <span className="legend-section ms-2">Bidder:</span>
        <span><span className="legend-window"></span> Bid Window</span>
        <span><span className="legend-marker"></span> Reveal Time</span>
        <span><span className="legend-dot bg-bid-submitted"></span> Bid Submitted</span>
        <span><span className="legend-dot bg-bid-failed"></span> Bid Failed</span>
        <span><span className="legend-dot bg-reveal-sent"></span> Reveal</span>
        <span><span className="legend-dot bg-reveal-failed"></span> Reveal Failed</span>
        <span><span className="legend-dot bg-reveal-skipped"></span> Reveal Withheld</span>
        <span className="legend-section ms-2">Builder API:</span>
        <span><span className="legend-dot bg-builder-api-delivered"></span> Call delivered</span>
        <span><span className="legend-dot bg-builder-api-pending"></span> Call pending</span>
      </div>
    </div>
  );
};

export const Legend = React.memo(LegendView);
