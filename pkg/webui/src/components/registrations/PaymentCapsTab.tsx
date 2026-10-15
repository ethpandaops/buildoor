import React from 'react';
import type { PolledList } from '../../hooks/usePolledList';
import { useSearchPager } from '../../hooks/useSearchPager';
import type { BuilderPreference } from '../../types';
import { CopyableHash } from '../CopyableHash';
import { RegistrationListCard } from './RegistrationListCard';
import type { Column } from './RegistrationTable';

function formatEth(gwei: number): string {
  // 1 ETH = 1e9 Gwei
  return (gwei / 1e9).toLocaleString(undefined, { maximumFractionDigits: 9 });
}

const COLUMNS: Column<BuilderPreference>[] = [
  {
    key: 'pubkey',
    header: 'Validator pubkey',
    mono: true,
    render: (p) => <CopyableHash value={p.validator_pubkey} full className="bd-reg-hash" />,
  },
  {
    key: 'gwei',
    header: 'Max execution payment (Gwei)',
    hint: (
      <>
        Highest <code>execution_payment</code> (trusted EL payment) the proposer accepts in a served bid. Caps the
        Builder API execution payment portion unless the preference limit is ignored; a proposer without an entry
        counts as 0.
      </>
    ),
    align: 'end',
    mono: true,
    render: (p) => p.max_execution_payment.toLocaleString(),
  },
  {
    key: 'eth',
    header: 'ETH',
    align: 'end',
    mono: true,
    muted: true,
    render: (p) => formatEth(p.max_execution_payment),
  },
];

export const PaymentCapsTab: React.FC<{ list: PolledList<BuilderPreference> }> = ({ list }) => {
  const pager = useSearchPager(list.items, (p) => [p.validator_pubkey, p.max_execution_payment]);

  return (
    <RegistrationListCard
      title="Payment caps"
      hint={
        <>
          Builder preferences proposers submitted to this builder through the Gloas Builder API (
          <code>submitBuilderPreferences</code>), kept per validator pubkey. Highest cap first.
        </>
      }
      list={list}
      pager={pager}
      searchPlaceholder="Search validator pubkey or max execution payment…"
      columns={COLUMNS}
      rowKey={(p) => p.validator_pubkey}
      notEnabledText="Builder API not enabled. Run with --builder-api-enabled to receive proposer submissions."
      emptyText="No payment caps received yet."
      noMatchText="No payment caps match your search."
    />
  );
};
