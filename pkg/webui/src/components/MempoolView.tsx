import React, { useEffect, useMemo, useState } from 'react';
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
import { usePageVisible } from './mempool/usePageVisible';
import type { TxPoolStats } from '../types';
import '../styles/mempool.css';

const PAGE_LIMIT = 50;
const PREVIEW_POLL_MS = 10000;
const BASE_PATH = '/mempool';

type MempoolTab = 'queue' | 'next-block' | 'el-mempool' | 'access' | 'settings';

const TAB_IDS: MempoolTab[] = ['queue', 'next-block', 'el-mempool', 'access', 'settings'];

// Each tab owns a sub path of /mempool (the queue is the bare path), so tabs
// are linkable and survive a reload.
function tabFromPath(path: string): MempoolTab {
  const sub = path.startsWith(BASE_PATH + '/') ? path.slice(BASE_PATH.length + 1) : '';
  return (TAB_IDS as string[]).includes(sub) ? (sub as MempoolTab) : 'queue';
}

function tabPath(tab: MempoolTab): string {
  return tab === 'queue' ? BASE_PATH : `${BASE_PATH}/${tab}`;
}

// MempoolView is the owned transaction pool page: state + ingress header
// with the accept switch and clear action, the pool aggregates, and tabs for
// the queued transactions, the next-block selection preview, /rpc access
// (API keys, callers, upstreams) and the pool settings summary. Pool
// contents, the preview, keys and callers are operator-only; settings are
// edited in the settings drawer.
export const MempoolView: React.FC = () => {
  const { txPoolStats, serviceStatus } = useEventStream();
  const { isLoggedIn } = useAuthContext();
  const { status: localStatus } = useLocalBuildStatus(serviceStatus?.txpool_enabled);
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
  const elMempool = useELMempool(elQuery, tab === 'el-mempool' && visible);
  const {
    data,
    loading,
    error,
    unauthorized,
    preview,
    previewError,
    previewUnauthorized,
    fetchPreview,
    clear,
    drop,
  } = useTxPool(query, tab === 'queue', txPoolStats?.version);

  // The preview feeds the Next block tab and the drain estimate of the
  // stats, so it refreshes on every pool change and periodically while the
  // page is visible. Without a login it would only answer 401.
  useEffect(() => {
    if (!isLoggedIn || !visible) return undefined;
    fetchPreview();
    const interval = setInterval(fetchPreview, PREVIEW_POLL_MS);
    return () => clearInterval(interval);
  }, [fetchPreview, isLoggedIn, visible, txPoolStats?.version]);

  const stats: TxPoolStats | null = txPoolStats ?? data?.stats ?? null;
  const enabled = stats?.enabled ?? serviceStatus?.txpool_enabled ?? false;
  const available = serviceStatus?.txpool_available ?? localStatus?.txpool.available ?? false;
  const ingressUrl = `${window.location.origin}/rpc`;
  const pending = stats?.pending ?? 0;

  const tabs: TabItem<MempoolTab>[] = [
    { id: 'queue', label: 'Queue', extra: <span className="bd-mp-tab-count">{pending.toLocaleString()}</span> },
    { id: 'next-block', label: 'Next block' },
    {
      id: 'el-mempool',
      label: 'EL mempool',
      extra: elMempool.data?.stats.active
        ? <span className="bd-mp-tab-count">{elMempool.data.stats.txs.toLocaleString()}</span>
        : undefined,
    },
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

      <MempoolStats stats={stats} gasLimit={preview?.gas_limit ?? 0} />

      <Tabs tabs={tabs} active={tab} onChange={(id) => navigate(tabPath(id))} label="Mempool sections" />

      <div className="bd-mp-tab-panel" role="tabpanel">
        {tab === 'queue' && (
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
            preview={preview}
            error={previewError}
            locked={!isLoggedIn || previewUnauthorized}
            onRefresh={fetchPreview}
          />
        )}
        {tab === 'el-mempool' && (
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
