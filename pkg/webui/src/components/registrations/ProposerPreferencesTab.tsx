import React, { useMemo, useState } from 'react';
import type { PolledList } from '../../hooks/usePolledList';
import { useSearchPager } from '../../hooks/useSearchPager';
import type { ProposerPreference } from '../../types';
import { CopyableHash } from '../CopyableHash';
import { RegistrationListCard } from './RegistrationListCard';
import type { Column } from './RegistrationTable';

// Client filter value for rows without a validator-ranges client name.
const UNKNOWN_CLIENT = '\u0000unknown';

const COLUMNS: Column<ProposerPreference>[] = [
  {
    key: 'slot',
    header: 'Slot',
    hint: 'The proposal slot the preference applies to.',
    align: 'end',
    mono: true,
    render: (p) => p.slot,
  },
  {
    key: 'validator_index',
    header: 'Validator index',
    align: 'end',
    mono: true,
    render: (p) => p.validator_index,
  },
  {
    key: 'client',
    header: 'Client',
    hint: 'Client name of the validator, from the validator-ranges mapping (— = unknown).',
    render: (p) => p.client_name || <span className="bd-faint">—</span>,
  },
  {
    key: 'fee_recipient',
    header: 'Fee recipient',
    mono: true,
    render: (p) => <CopyableHash value={p.fee_recipient} full className="bd-reg-hash" />,
  },
  {
    key: 'gas_limit',
    header: 'Gas limit',
    hint: "The proposer's target gas limit. The bid's gas limit is the parent gas limit stepped toward this target (within the per-block adjustment bound).",
    align: 'end',
    mono: true,
    render: (p) => p.target_gas_limit.toLocaleString(),
  },
];

export const ProposerPreferencesTab: React.FC<{ list: PolledList<ProposerPreference> }> = ({ list }) => {
  const [client, setClient] = useState('');

  const clients = useMemo(() => {
    const names = new Set<string>();
    let unknown = false;
    for (const p of list.items) {
      if (p.client_name) names.add(p.client_name);
      else unknown = true;
    }
    return { names: Array.from(names).sort((a, b) => a.localeCompare(b)), unknown };
  }, [list.items]);

  const pager = useSearchPager(
    list.items,
    (p) => [p.slot, p.validator_index, p.fee_recipient, p.client_name],
    {
      filterKey: client,
      predicate: client
        ? (p) => (client === UNKNOWN_CLIENT ? !p.client_name : p.client_name === client)
        : undefined,
    },
  );

  const filters =
    clients.names.length > 0 ? (
      <select
        className="form-select form-select-sm bd-reg-filter"
        aria-label="Filter by client"
        value={client}
        onChange={(e) => setClient(e.target.value)}
      >
        <option value="">All clients</option>
        {clients.names.map((name) => (
          <option key={name} value={name}>
            {name}
          </option>
        ))}
        {clients.unknown && <option value={UNKNOWN_CLIENT}>Unknown client</option>}
      </select>
    ) : null;

  return (
    <RegistrationListCard
      title="Proposer preferences"
      hint="Gloas proposer preferences received from the gossip stream, first one per slot. They set the fee recipient and gas limit target of the payloads buildoor builds for that slot, and the p2p bidder only bids for slots with a cached preference (unless an action plan ignores missing preferences). Newest slot first."
      list={list}
      pager={pager}
      searchPlaceholder="Search slot, validator index, client or fee recipient…"
      filters={filters}
      columns={COLUMNS}
      rowKey={(p) => `${p.slot}-${p.validator_index}`}
      notEnabledText="Proposer preferences service not enabled. It runs when the Gloas fork is scheduled on this network."
      emptyText="No proposer preferences received yet."
      noMatchText="No preferences match your search."
    />
  );
};
