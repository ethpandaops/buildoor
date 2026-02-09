import React from 'react';

export const Legend: React.FC = () => {
  return (
    <div className="mt-2">
      <div className="timeline-legend d-flex flex-wrap gap-2 small">
        <span className="legend-section">Chain:</span>
        <span><span className="legend-dot bg-block-received"></span> Block</span>
        <span><span className="legend-dot bg-payload-received"></span> Payload Envelope</span>
        <span><span className="legend-dot bg-external-bid"></span> External Bid</span>
        <span className="legend-section ms-2">Network:</span>
        <span><span className="legend-dot bg-head-votes"></span> Head Votes</span>
        <span className="legend-section ms-2">Builder:</span>
        <span><span className="legend-dot bg-payload-created"></span> Payload Created</span>
        <span><span className="legend-dot bg-bid-submitted"></span> Bid Submitted</span>
        <span><span className="legend-dot bg-bid-failed"></span> Bid Failed</span>
        <span><span className="legend-dot bg-reveal-sent"></span> Reveal</span>
        <span><span className="legend-dot bg-reveal-failed"></span> Reveal Failed</span>
        <span className="legend-section ms-2">Builder API:</span>
        <span><span className="legend-dot bg-builder-api-submitted"></span> Submitted</span>
        <span><span className="legend-dot bg-builder-api-failed"></span> Failed</span>
      </div>
    </div>
  );
};
