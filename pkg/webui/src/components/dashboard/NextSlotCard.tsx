import React from 'react';
import { setView } from '../../stores/viewStore';
import type { SlotOutlook, SlotPlan } from '../../types';
import { count } from './format';

interface NextSlotCardProps {
  outlook: SlotOutlook | null;
}

const MODE_TEXT: Record<string, string> = { custom: 'custom', disabled: 'suppressed' };

// planSummary lists what a slot plan changes against the global baseline.
function planSummary(plan: SlotPlan): string[] {
  const parts: string[] = [];
  if (plan.bid) parts.push(`p2p bid ${MODE_TEXT[plan.bid.mode] ?? plan.bid.mode}`);
  if (plan.builder_api) parts.push(`Builder API ${MODE_TEXT[plan.builder_api.mode] ?? plan.builder_api.mode}`);
  if (plan.reveal) parts.push(plan.reveal.mode === 'disabled' ? 'reveal withheld' : `reveal ${plan.reveal.mode}`);
  if (plan.build?.reorg_parent_payload) parts.push('builds on the grandparent');
  if (plan.build?.candidates && Object.keys(plan.build.candidates).length > 0) parts.push('candidate overrides');
  if (plan.build?.local) parts.push('testing build overrides');
  if (plan.transforms) parts.push('jq transforms');
  if (plan.ignore_rules && parts.length === 0) parts.push('rules ignored, global baseline');
  return parts;
}

// NextSlotCard shows what the next slot will run under before its build
// starts: the proposer, what it told buildoor, and the slot's action plan.
export const NextSlotCard: React.FC<NextSlotCardProps> = ({ outlook }) => {
  const plan = outlook?.plan;
  const changes = plan ? planSummary(plan) : [];

  return (
    <section className="card db-card db-next" aria-labelledby="db-next-title">
      <div className="db-card-head">
        <h2 className="bd-card-title" id="db-next-title">
          Next slot{outlook ? ` · ${count(outlook.slot)}` : ''}
        </h2>
        {outlook?.frozen && <span className="bd-tag bd-tone-neutral">frozen</span>}
      </div>
      {!outlook ? (
        <p className="bd-muted small mb-0">Waiting for the next slot…</p>
      ) : (
        <dl className="bd-kv">
          <dt>Proposer</dt>
          <dd>
            {outlook.proposer_index !== undefined ? `#${count(outlook.proposer_index)}` : 'unknown'}
            {outlook.proposer_client ? ` · ${outlook.proposer_client}` : ''}
          </dd>
          {outlook.builder_api_registered !== undefined && (
            <>
              <dt>Builder API</dt>
              <dd>
                {outlook.builder_api_registered
                  ? `registered · ${count(outlook.registered_gas_limit)} gas`
                  : 'not registered'}
              </dd>
            </>
          )}
          {outlook.proposer_prefs !== undefined && (
            <>
              <dt>Proposer prefs</dt>
              <dd>{outlook.proposer_prefs ? `received · ${count(outlook.prefs_gas_limit)} gas` : 'none yet'}</dd>
            </>
          )}
          <dt>Plan</dt>
          <dd title={changes.join(', ')}>
            {!plan
              ? 'global baseline'
              : plan.rule_id
                ? `rule ${plan.rule_id}`
                : changes.length > 0
                  ? 'explicit plan'
                  : 'global baseline'}
          </dd>
          {changes.length > 0 && (
            <>
              <dt>Changes</dt>
              <dd title={changes.join(', ')}>{changes.join(' · ')}</dd>
            </>
          )}
        </dl>
      )}
      <button type="button" className="bd-btn bd-btn-sm db-next-link" onClick={() => setView('action-plan')}>
        Open action plan
      </button>
    </section>
  );
};
