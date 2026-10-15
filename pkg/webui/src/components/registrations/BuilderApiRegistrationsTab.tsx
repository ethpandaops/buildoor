import React from 'react';
import type { PolledList } from '../../hooks/usePolledList';
import { useSearchPager } from '../../hooks/useSearchPager';
import type { ValidatorRegistration } from '../../types';
import { CopyableHash } from '../CopyableHash';
import { RegistrationListCard } from './RegistrationListCard';
import type { Column } from './RegistrationTable';

const COLUMNS: Column<ValidatorRegistration>[] = [
  {
    key: 'pubkey',
    header: 'Pubkey',
    mono: true,
    render: (v) => <CopyableHash value={v.pubkey} full className="bd-reg-hash" />,
  },
  {
    key: 'fee_recipient',
    header: 'Fee recipient',
    hint: 'Address the validator registered; pre-Gloas payloads built for its proposals pay this fee recipient.',
    mono: true,
    render: (v) => <CopyableHash value={v.fee_recipient} full className="bd-reg-hash" />,
  },
  {
    key: 'gas_limit',
    header: 'Gas limit',
    hint: "Gas limit target from the registration. Shown for reference only: buildoor's pre-Gloas builds do not apply it.",
    align: 'end',
    mono: true,
    render: (v) => v.gas_limit.toLocaleString(),
  },
  {
    key: 'timestamp',
    header: 'Registered',
    hint: 'Timestamp inside the signed registration message.',
    align: 'end',
    muted: true,
    render: (v) => new Date(v.timestamp * 1000).toLocaleString(),
  },
];

export const BuilderApiRegistrationsTab: React.FC<{ list: PolledList<ValidatorRegistration> }> = ({ list }) => {
  const pager = useSearchPager(list.items, (v) => [v.pubkey, v.fee_recipient]);

  return (
    <RegistrationListCard
      title="Builder API registrations"
      hint={
        <>
          Validator registrations (<code>registerValidators</code>) received through the Builder API, kept per
          pubkey. Newest registration first.
        </>
      }
      list={list}
      pager={pager}
      searchPlaceholder="Search pubkey or fee recipient…"
      columns={COLUMNS}
      rowKey={(v) => v.pubkey}
      notEnabledText="Builder API not enabled. Run with --builder-api-enabled to receive validator registrations."
      emptyText="No validators registered yet."
      noMatchText="No validators match your search."
    />
  );
};
