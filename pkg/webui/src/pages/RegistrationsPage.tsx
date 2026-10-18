import React from 'react';
import { BuilderApiRegistrationsTab } from '../components/registrations/BuilderApiRegistrationsTab';
import { PaymentCapsTab } from '../components/registrations/PaymentCapsTab';
import { ProposerPreferencesTab } from '../components/registrations/ProposerPreferencesTab';
import { Tabs, type TabItem } from '../components/ui/Tabs';
import { useBuilderPreferences } from '../hooks/useBuilderPreferences';
import type { PolledList } from '../hooks/usePolledList';
import { useProposerPreferences } from '../hooks/useProposerPreferences';
import { useValidators } from '../hooks/useValidators';
import { getViewPath, navigate, usePath } from '../stores/viewStore';
import '../styles/registrations.css';

type RegistrationTab = 'builder-api' | 'proposer-preferences' | 'payment-caps';

const BASE_PATH = getViewPath('registrations');

// Sub path of each tab below /registrations; the first tab owns the bare path.
const TAB_PATHS: Record<RegistrationTab, string> = {
  'builder-api': BASE_PATH,
  'proposer-preferences': `${BASE_PATH}/proposer-preferences`,
  'payment-caps': `${BASE_PATH}/payment-caps`,
};

function tabFromPath(path: string): RegistrationTab {
  for (const [tab, tabPath] of Object.entries(TAB_PATHS)) {
    if (path === tabPath) return tab as RegistrationTab;
  }
  return 'builder-api';
}

function CountBadge<T>({ list }: { list: PolledList<T> }) {
  if (list.loading) return <span className="bd-tag bd-tone-neutral">…</span>;
  if (list.notEnabled) {
    return (
      <span className="bd-tag bd-tone-neutral" title="Not enabled">
        off
      </span>
    );
  }
  if (list.error && list.items.length === 0) {
    return (
      <span className="bd-tag bd-tone-danger" title={list.error}>
        !
      </span>
    );
  }
  return <span className="bd-tag bd-tone-neutral">{list.items.length.toLocaleString()}</span>;
}

// RegistrationsPage shows what proposers told buildoor about themselves, one
// tab per source. All three lists load up front so the tab badges carry live
// counts; the URL follows the tab, so tabs are linkable and history works.
const RegistrationsPage: React.FC = () => {
  const path = usePath();
  const active = tabFromPath(path);

  const validators = useValidators();
  const proposerPrefs = useProposerPreferences();
  const paymentCaps = useBuilderPreferences();

  const tabs: TabItem<RegistrationTab>[] = [
    { id: 'builder-api', label: 'Builder API registrations', extra: <CountBadge list={validators} /> },
    { id: 'proposer-preferences', label: 'Proposer preferences', extra: <CountBadge list={proposerPrefs} /> },
    { id: 'payment-caps', label: 'Payment caps', extra: <CountBadge list={paymentCaps} /> },
  ];

  return (
    <div className="bd-reg-page">
      <header className="mb-3">
        <h1 className="bd-page-title">Validator registrations</h1>
        <p className="bd-page-lead">
          What proposers told buildoor about themselves: fee recipients and gas limits registered through the
          Builder API, gossiped Gloas proposer preferences, and the payment caps that bound the execution payment of
          served Gloas bids.
        </p>
      </header>

      <Tabs tabs={tabs} active={active} onChange={(tab) => navigate(TAB_PATHS[tab])} label="Registration lists" />

      <div className="mt-3">
        {active === 'builder-api' && <BuilderApiRegistrationsTab list={validators} />}
        {active === 'proposer-preferences' && <ProposerPreferencesTab list={proposerPrefs} />}
        {active === 'payment-caps' && <PaymentCapsTab list={paymentCaps} />}
      </div>
    </div>
  );
};

export default RegistrationsPage;
