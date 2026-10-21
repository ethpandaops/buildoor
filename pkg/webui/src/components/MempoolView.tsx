import React, { useMemo, useState } from 'react';
import { useEventStream } from '../hooks/useEventStream';
import { useTxPool } from '../hooks/useTxPool';
import { useLocalBuildStatus } from '../hooks/useLocalBuildStatus';
import { useAuthContext } from '../context/AuthContext';
import { navigate, usePath } from '../stores/viewStore';
import { Tabs, type TabItem } from './ui/Tabs';
import { FlowSettingsSummary } from './settings/FlowSettingsSummary';
import { MempoolHeader } from './mempool/MempoolHeader';
import { MempoolStats } from './mempool/MempoolStats';
import { QueueTab } from './mempool/QueueTab';
import { NextBlockTab } from './mempool/NextBlockTab';
import { AccessTab } from './mempool/AccessTab';
import { ELMempoolTab } from './mempool/ELMempoolTab';
import { useELMempool } from '../hooks/useELMempool';
import { useNextBlock } from '../hooks/useNextBlock';
import { usePageVisible } from './mempool/usePageVisible';
import type { TxPoolStats } from '../types';
import '../styles/mempool.css';

const PAGE_LIMIT = 50;
const BASE_PATH = '/mempool';

type MempoolTab = 'next-block' | 'public' | 'private' | 'access' | 'settings';

const TAB_IDS: MempoolTab[] = ['next-block', 'public', 'private', 'access', 'settings'];

// Former sub paths of the tabs, kept working.
const RETIRED_TABS: Record<string, MempoolTab> = { queue: 'private', 'el-mempool': 'public' };

// Each tab owns a sub path of /mempool (the next block is the bare path), so
// tabs are linkable and survive a reload.
function tabFromPath(path: string): MempoolTab {
  const sub = path.startsWith(BASE_PATH + '/') ? path.slice(BASE_PATH.length + 1) : '';
  if ((TAB_IDS as string[]).includes(sub)) return sub as MempoolTab;
  return RETIRED_TABS[sub] ?? 'next-block';
}

function tabPath(tab: MempoolTab): string {
  return tab === 'next-block' ? BASE_PATH : `${BASE_PATH}/${tab}`;
}

// MempoolView is the mempool page: state + ingress header with the accept
// switch and clear action, the private pool's aggregates, and tabs for the
// next block (the latest pick and the next slot's estimate), the public (EL)
// mempool, the private mempool (buildoor's own pool), /rpc access (API keys,
// callers, upstreams) and the pool settings summary. Private transactions,
// keys and callers are operator-only; settings are edited in the settings
// drawer.
export const MempoolView: React.FC = () => {
  const { txPoolStats, serviceStatus, currentSlot, slotStates, nextBlock: streamedEstimate } = useEventStream();
  const { isLoggedIn } = useAuthContext();
  const { status: localStatus } = useLocalBuildStatus();
  const visible = usePageVisible();
  const tab = tabFromPath(usePath());

  const [offset, setOffset] = useState(0);
  const [sender, setSender] = useState('');
  const [sort, setSort] = useState('arrival');

  const query = useMemo(() => ({ offset, limit: PAGE_LIMIT, sender: sender.trim(), sort }), [offset, sender, sort]);

  // The EL mempool tab keeps its own listing state.
  const [elOffset, setElOffset] = useState(0);
  const [elSender, setElSender] = useState('');
  const [elSort, setElSort] = useState('tip');
  const elQuery = useMemo(
    () => ({ offset: elOffset, limit: PAGE_LIMIT, sender: elSender.trim(), sort: elSort }),
    [elOffset, elSender, elSort],
  );
  const elMempool = useELMempool(elQuery, tab === 'public' && visible);
  const { data, loading, error, unauthorized, clear, drop } = useTxPool(query, tab === 'private', txPoolStats?.version);

  // The estimate arrives over the event stream (new head, new slot, pool
  // changes); logged-in readers get its transactions once per estimate.
  const nextBlock = useNextBlock(streamedEstimate, visible && tab === 'next-block');

  const stats: TxPoolStats | null = txPoolStats ?? data?.stats ?? null;
  const enabled = stats?.enabled ?? serviceStatus?.txpool_enabled ?? false;
  const available = serviceStatus?.txpool_available ?? localStatus?.txpool.available ?? false;
  const ingressUrl = `${window.location.origin}/rpc`;
  const pending = stats?.pending ?? 0;

  const tabs: TabItem<MempoolTab>[] = [
    { id: 'next-block', label: 'Next block' },
    {
      id: 'public',
      label: 'Public mempool',
      extra: elMempool.data?.stats.active
        ? <span className="bd-mp-tab-count">{elMempool.data.stats.txs.toLocaleString()}</span>
        : undefined,
    },
    { id: 'private', label: 'Private mempool', extra: <span className="bd-mp-tab-count">{pending.toLocaleString()}</span> },
    { id: 'access', label: 'Access' },
    { id: 'settings', label: 'Settings' },
  ];

  return (
    <div className="bd-mp-page">
      <MempoolHeader
        ingressUrl={ingressUrl}
        enabled={enabled}
        available={available}
        unavailableReason={localStatus?.txpool.reason}
        pending={pending}
        onClear={clear}
      />

      <MempoolStats stats={stats} gasLimit={nextBlock?.estimates[0]?.gas_limit ?? 0} />

      <Tabs tabs={tabs} active={tab} onChange={(id) => navigate(tabPath(id))} label="Mempool sections" />

      <div className="bd-mp-tab-panel" role="tabpanel">
        {tab === 'private' && (
          <QueueTab
            ingressUrl={ingressUrl}
            data={data}
            loading={loading}
            error={error}
            unauthorized={unauthorized}
            isLoggedIn={isLoggedIn}
            pageLimit={PAGE_LIMIT}
            offset={offset}
            onOffsetChange={setOffset}
            sender={sender}
            onSenderChange={setSender}
            sort={sort}
            onSortChange={setSort}
            onDrop={drop}
          />
        )}
        {tab === 'next-block' && (
          <NextBlockTab
            currentSlot={currentSlot}
            slotStates={slotStates}
            estimates={nextBlock}
            isLoggedIn={isLoggedIn}
          />
        )}
        {tab === 'public' && (
          <ELMempoolTab
            data={elMempool.data}
            fees={elMempool.fees}
            error={elMempool.error}
            loading={elMempool.loading}
            isLoggedIn={isLoggedIn}
            pageLimit={PAGE_LIMIT}
            offset={elOffset}
            onOffsetChange={setElOffset}
            sender={elSender}
            onSenderChange={setElSender}
            sort={elSort}
            onSortChange={setElSort}
          />
        )}
        {tab === 'access' && <AccessTab />}
        {tab === 'settings' && (
          <FlowSettingsSummary flow="mempool" tab="pool" />
        )}
      </div>
    </div>
  );
};
