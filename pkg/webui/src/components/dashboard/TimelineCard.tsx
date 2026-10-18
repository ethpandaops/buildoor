import React from 'react';
import type { ChainInfo, Config, ServiceStatus, SlotState, VoteCoverage } from '../../types';
import { SlotTimeline } from '../SlotTimeline';
import { Legend } from '../Legend';

interface TimelineCardProps {
  chainInfo: ChainInfo | null;
  slotStates: Record<number, SlotState>;
  slotConfigs: Record<number, Config>;
  slotServiceStatuses: Record<number, ServiceStatus>;
  config: Config | null;
  serviceStatus: ServiceStatus | null;
  voteCoverage: VoteCoverage | null;
}

// TimelineCard frames the slot timeline with its vote coverage badges and
// the legend.
export const TimelineCard: React.FC<TimelineCardProps> = ({
  chainInfo,
  slotStates,
  slotConfigs,
  slotServiceStatuses,
  config,
  serviceStatus,
  voteCoverage,
}) => (
  <section className="card db-timeline" aria-labelledby="db-timeline-title">
    <div className="db-card-head">
      <h2 className="bd-card-title" id="db-timeline-title">
        Slot timeline
      </h2>
      <div className="d-flex gap-2 ms-auto">
        {voteCoverage?.low && (
          <span className="badge bg-warning text-dark coverage-warning">
            <i className="fas fa-triangle-exclamation me-1"></i>
            Low vote coverage
            <span className="coverage-callout">
              <span className="coverage-callout-title">
                Attestation subnet coverage low
              </span>
              Only {voteCoverage.seen_pct.toFixed(0)}% of the attesters that landed
              on chain were seen as raw single attestations (last{' '}
              {voteCoverage.slots} blocks)
              {voteCoverage.sampled_attesters > 0 && (
                <>
                  , and only {voteCoverage.sampled_seen_pct.toFixed(0)}% within the
                  subnets the beacon node delivers
                </>
              )}
              . Too few votes are seen to estimate participation: the head-vote
              graph has been hidden, and{' '}
              <strong>vote-threshold-gated payload reveals are unreliable</strong>{' '}
              — the reveal vote gate opens late or never (payloads may be
              withheld at slot end). Use a time-based reveal gate or fix the
              subnet subscription.
              <br />
              Subscribe to all subnets via <code>--subscribe-all-subnets</code>{' '}
              (Lighthouse/Prysm/Nimbus/Grandine),{' '}
              <code>--p2p-subscribe-all-subnets-enabled</code> (Teku) or{' '}
              <code>--subscribeAllSubnets</code> (Lodestar).
            </span>
          </span>
        )}
        {voteCoverage && voteCoverage.estimated && !voteCoverage.low && (
          <span className="badge bg-info text-dark coverage-warning">
            <i className="fas fa-chart-pie me-1"></i>
            Estimated votes
            <span className="coverage-callout">
              <span className="coverage-callout-title">
                Head votes estimated from sampled subnets
              </span>
              The beacon node only delivers single attestations for some subnets
              ({voteCoverage.seen_pct.toFixed(0)}% of the attesters that landed on
              chain were seen, last {voteCoverage.slots} blocks). Participation is
              extrapolated from the committees on those subnets, which hold{' '}
              {voteCoverage.sampled_pct.toFixed(0)}% of the attesters and whose
              votes were seen at {voteCoverage.sampled_seen_pct.toFixed(0)}%. The
              head-vote graph and vote-gated payload reveals use this estimate.
            </span>
          </span>
        )}
      </div>
    </div>
    <SlotTimeline
      chainInfo={chainInfo}
      slotStates={slotStates}
      slotConfigs={slotConfigs}
      slotServiceStatuses={slotServiceStatuses}
      currentConfig={config}
      serviceStatus={serviceStatus}
      hideHeadVotes={voteCoverage?.low ?? false}
    />
    <Legend />
  </section>
);
