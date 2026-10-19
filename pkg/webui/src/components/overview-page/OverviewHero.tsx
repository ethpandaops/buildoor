import React from 'react';
import { Tag, type Tone } from '../ui/Tag';
import type { ChainInfo, Config, ServiceStatus, SlotState, Stats } from '../../types';

interface OverviewHeroProps {
  config: Config | null;
  chainInfo: ChainInfo | null;
  serviceStatus: ServiceStatus | null;
  stats: Stats | null;
  slotStates: Record<number, SlotState>;
  currentSlot: number;
  mempoolOpen: boolean;
}

// Slots looked back on to decide whether the builder is currently building.
const BUILDING_WINDOW = 4;

function buildingTag(slotStates: Record<number, SlotState>, currentSlot: number): { tone: Tone; text: string; title: string } {
  for (let slot = currentSlot; slot > currentSlot - BUILDING_WINDOW; slot--) {
    const state = slotStates[slot];
    if (state?.targets && Object.keys(state.targets).length > 0) {
      return { tone: 'ok', text: 'Building', title: `Built a payload within the last ${BUILDING_WINDOW} slots` };
    }
  }
  return { tone: 'neutral', text: 'Idle', title: `No payload built within the last ${BUILDING_WINDOW} slots` };
}

function epbsTag(status: ServiceStatus, gloasEpoch: number | null | undefined): { tone: Tone; text: string; title?: string } {
  if (!status.epbs_available) {
    return { tone: 'neutral', text: 'ePBS · not scheduled', title: 'The Gloas fork is not scheduled on this network' };
  }
  const state = status.epbs_registration_state;
  if (state === 'waiting_gloas') {
    const text = gloasEpoch != null ? `ePBS from epoch ${gloasEpoch.toLocaleString()}` : 'ePBS · waiting for Gloas';
    return { tone: 'info', text, title: 'p2p bidding starts once the Gloas fork activates' };
  }
  if (!status.epbs_enabled) {
    return { tone: 'neutral', text: 'ePBS · off' };
  }
  if (state === 'registered') {
    return { tone: 'ok', text: 'ePBS · active' };
  }
  return { tone: 'warn', text: `ePBS · ${(state || 'unknown').replace(/_/g, ' ')}`, title: 'Builder registration state' };
}

export const OverviewHero: React.FC<OverviewHeroProps> = ({
  config,
  chainInfo,
  serviceStatus,
  stats,
  slotStates,
  currentSlot,
  mempoolOpen,
}) => {
  const builderAPI = !!serviceStatus?.builder_api_available && !!serviceStatus.builder_api_enabled;
  const epbsScheduled = !!serviceStatus?.epbs_available;
  const everySlot = !config || config.schedule.mode === 'all';
  const building = buildingTag(slotStates, currentSlot);
  const validators = stats?.builder_api_registered_validators ?? 0;
  const network = chainInfo?.network ? chainInfo.network.charAt(0).toUpperCase() + chainInfo.network.slice(1) : '';

  // Gloas is "active" from its fork epoch on; before it the p2p bids are a
  // promise with a date, after it they are what the builder does.
  const gloasEpoch = chainInfo?.gloas_fork_epoch ?? null;
  const currentEpoch = chainInfo && currentSlot ? Math.floor(currentSlot / chainInfo.slots_per_epoch) : null;
  const gloasActive = gloasEpoch !== null && currentEpoch !== null && currentEpoch >= gloasEpoch;
  const p2pBidding = epbsScheduled && !!serviceStatus?.epbs_enabled;
  const mempoolAvailable = serviceStatus?.txpool_available === true;

  let offer: string;
  if (gloasActive) {
    offer = p2pBidding && builderAPI
      ? 'It bids for slots over p2p under enshrined PBS and serves bids to proposers that call it directly through the Builder API.'
      : p2pBidding
        ? 'It bids for slots over p2p under enshrined PBS.'
        : builderAPI
          ? 'It serves bids to proposers that call it directly through the Builder API.'
          : 'It is not offering its blocks to proposers at the moment.';
  } else {
    const p2pFuture = p2pBidding
      ? gloasEpoch !== null
        ? ` From the Gloas fork (epoch ${gloasEpoch.toLocaleString()}) on, it also bids for slots over p2p under enshrined PBS.`
        : ' Once Gloas activates, it also bids for slots over p2p under enshrined PBS.'
      : '';
    offer = builderAPI
      ? `Proposers get its blocks through the Builder API.${p2pFuture}`
      : p2pFuture
        ? p2pFuture.trim()
        : 'It is not offering its blocks to proposers at the moment.';
  }

  const mempool = mempoolOpen
    ? 'Transactions sent to its mempool stay private until a block it built is published.'
    : mempoolAvailable
      ? 'Its private mempool is not accepting transactions at the moment.'
      : '';

  return (
    <section className="ov-hero" aria-labelledby="ov-hero-title">
      <div className="ov-hero-text">
        <h1 id="ov-hero-title" className="ov-hero-title">
          An open block builder{network ? ` for ${network}` : ''}
        </h1>
        <p className="ov-hero-lead">
          buildoor builds execution payloads {everySlot ? 'every slot' : 'on a configured slot schedule'}. {offer}
          {mempool && ` ${mempool}`}
        </p>
      </div>
      {serviceStatus && (
        <ul className="ov-hero-tags" aria-label="Builder status">
          <li>
            <Tag tone={building.tone} title={building.title}>
              <span className="ov-dot" aria-hidden="true" />
              {building.text}
            </Tag>
          </li>
          {builderAPI && (
            <li>
              <Tag tone="accent" title="Validators registered with this builder's Builder API">
                Builder API · {validators.toLocaleString()} validator{validators === 1 ? '' : 's'}
              </Tag>
            </li>
          )}
          {serviceStatus.builder_api_available && !serviceStatus.builder_api_enabled && (
            <li>
              <Tag tone="neutral">Builder API · paused</Tag>
            </li>
          )}
          <li>
            {(() => {
              const tag = epbsTag(serviceStatus, chainInfo?.gloas_fork_epoch);
              return (
                <Tag tone={tag.tone} title={tag.title}>
                  {tag.text}
                </Tag>
              );
            })()}
          </li>
        </ul>
      )}
    </section>
  );
};
