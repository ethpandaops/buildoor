import React from 'react';
import { useEventStream } from '../hooks/useEventStream';
import { setView } from '../stores/viewStore';
import { OverviewHero } from '../components/overview-page/OverviewHero';
import { KpiTiles } from '../components/overview-page/KpiTiles';
import { SlotStrip } from '../components/overview-page/SlotStrip';
import { BlocksWonCard } from '../components/overview-page/BlocksWonCard';
import { PrivateTxCard } from '../components/overview-page/PrivateTxCard';
import { ValidatorsCard } from '../components/overview-page/ValidatorsCard';
import { useRecentWins, useStripResults } from '../components/overview-page/useOverviewData';
import '../styles/overview.css';

// OverviewPage is the public landing page: what this builder is, what it has
// done recently and how to use it. It reads the shared event stream and makes
// two small REST requests per stream connection (plus one per won block); it
// never polls.
const OverviewPage: React.FC = () => {
  const {
    config,
    chainInfo,
    stats,
    builderInfo,
    builderKeys,
    serviceStatus,
    txPoolStats,
    currentSlot,
    slotStates,
    connectionGeneration,
  } = useEventStream();

  const wins = useRecentWins(connectionGeneration);
  const stripResults = useStripResults(connectionGeneration, currentSlot);

  const mempoolOpen = !!serviceStatus?.txpool_available && !!serviceStatus.txpool_enabled;
  const builderAPI = !!serviceStatus?.builder_api_available;
  const epbsScheduled = !!serviceStatus?.epbs_available;

  return (
    <div className="ov-page">
      <OverviewHero
        config={config}
        chainInfo={chainInfo}
        serviceStatus={serviceStatus}
        stats={stats}
        slotStates={slotStates}
        currentSlot={currentSlot}
        mempoolOpen={mempoolOpen}
      />

      <KpiTiles
        stats={stats}
        winsTotal={wins.total}
        retentionEpochs={config?.slot_result_retention_epochs}
        mempoolOpen={mempoolOpen}
        txPoolStats={txPoolStats}
      />

      <SlotStrip
        currentSlot={currentSlot}
        slotStates={slotStates}
        results={stripResults}
        recentWins={wins.entries}
      />

      <div className="ov-columns">
        <BlocksWonCard
          entries={wins.entries}
          total={wins.total}
          loaded={wins.loaded}
          onShowAll={() => setView('bids-won')}
        />
        <div className="ov-side">
          {serviceStatus?.txpool_available !== false && <PrivateTxCard open={mempoolOpen} txpool={config?.txpool} />}
          {(builderAPI || epbsScheduled) && (
            <ValidatorsCard
              builderAPI={builderAPI}
              epbsScheduled={epbsScheduled}
              builderUrl={config?.builder_api?.builder_url}
              builderPubkey={builderInfo?.builder_pubkey}
              builderKeys={builderKeys}
            />
          )}
        </div>
      </div>

      <footer className="ov-footer">
        <nav aria-label="More">
          <a
            href="/dashboard"
            onClick={(e) => {
              e.preventDefault();
              setView('dashboard');
            }}
          >
            Operator view
          </a>
          <a
            href="/api-docs"
            onClick={(e) => {
              e.preventDefault();
              setView('api-docs');
            }}
          >
            API reference
          </a>
          <a href="https://github.com/ethpandaops/buildoor" target="_blank" rel="noopener noreferrer">
            Source on GitHub <i className="fas fa-arrow-up-right-from-square" aria-hidden="true" />
          </a>
        </nav>
      </footer>
    </div>
  );
};

export default OverviewPage;
